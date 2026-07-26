package adapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/api"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// itemOperationService is the narrow port (ISP) OperationsAPIHandlers
// depends on for the three item placement transitions, satisfied by
// *app.OperationService. Distinct from items_web.go's own itemOperator
// (RemoveFromBin/ReturnToBin only, no AddToBin — the item detail page never
// offers "add to bin" as a control): each handler group depends only on the
// methods it uses, per ISP, even where the concrete service is shared.
type itemOperationService interface {
	AddToBin(ctx context.Context, actor identity.Principal, itemID domain.ItemID, binID domain.BinID) (app.Operation, error)
	RemoveFromBin(ctx context.Context, actor identity.Principal, itemID domain.ItemID, note *string) (app.Operation, error)
	ReturnToBin(ctx context.Context, actor identity.Principal, itemID domain.ItemID, binID domain.BinID, note *string) (app.Operation, error)
}

// binMoveService is the narrow port (ISP) OperationsAPIHandlers depends on
// to drive the bin-relocation endpoint, satisfied by *app.BinMover.
type binMoveService interface {
	Move(ctx context.Context, actor identity.Principal, binID domain.BinID, target domain.LocationID) (app.MoveResult, error)
}

// itemDetailReader is the narrow port (ISP) OperationsAPIHandlers depends on
// to re-read an item's current, visibility-scoped state for an
// idempotent-retry response (AC 4) — the domain guards already fail a
// retry inside its own transaction before any event row is appended, so
// this read never races a real write; it only decides whether the retry's
// requested end state already holds. Satisfied by *app.ItemQueryService (a
// superset, via Detail).
type itemDetailReader interface {
	Detail(ctx context.Context, viewer identity.Principal, id domain.ItemID) (*domain.ItemDetailResult, error)
}

// OperationsAPIHandlers serves NSTR-55's item/bin state-transition
// endpoints — add-to-bin, check-out, return, move-bin, and the
// return-request lifecycle — every one of them a thin JSON wrapper over the
// exact same app services the web detail/bin pages already drive
// (OperationService, BinMover, ReturnRequestService): no domain or app rule
// is duplicated here, so web and API can never disagree on what a valid
// transition is (AC 1). Every route is registered on the api.Router NSTR-53
// mounts behind its bearer-only gate; this type carries no session and no
// CSRF dependency, matching Locations/Bins/ItemsAPIHandlers.
type OperationsAPIHandlers struct {
	operations     itemOperationService
	mover          binMoveService
	returnRequests returnRequestOperator
	detail         itemDetailReader
	logger         *slog.Logger
}

// NewOperationsAPIHandlers constructs OperationsAPIHandlers. Every
// dependency is required; a missing one panics at construction time,
// matching every other WebHandlers/APIHandlers constructor in this
// codebase.
func NewOperationsAPIHandlers(operations itemOperationService, mover binMoveService, returnRequests returnRequestOperator, detail itemDetailReader, logger *slog.Logger) *OperationsAPIHandlers {
	if operations == nil {
		panic("storage/adapter: NewOperationsAPIHandlers requires a non-nil itemOperationService")
	}
	if mover == nil {
		panic("storage/adapter: NewOperationsAPIHandlers requires a non-nil binMoveService")
	}
	if returnRequests == nil {
		panic("storage/adapter: NewOperationsAPIHandlers requires a non-nil returnRequestOperator")
	}
	if detail == nil {
		panic("storage/adapter: NewOperationsAPIHandlers requires a non-nil itemDetailReader")
	}
	if logger == nil {
		panic("storage/adapter: NewOperationsAPIHandlers requires a non-nil logger")
	}
	return &OperationsAPIHandlers{operations: operations, mover: mover, returnRequests: returnRequests, detail: detail, logger: logger}
}

// Routes registers the operation routes on mux — an api.Registrar (DIP), see
// LocationsAPIHandlers.Routes' own doc for why. NSTR-55's own
// cancel-return-request route is not in the ticket's original endpoint
// list, but AC 1 requires every web transition to be reachable here too,
// and the item detail page's return-request control does offer cancel
// (return_requests_web.go).
func (h *OperationsAPIHandlers) Routes(mux api.Registrar) {
	mux.HandleFunc("POST /api/v1/items/{id}/add-to-bin", h.AddToBin)
	mux.HandleFunc("POST /api/v1/items/{id}/check-out", h.CheckOut)
	mux.HandleFunc("POST /api/v1/items/{id}/return", h.Return)
	mux.HandleFunc("POST /api/v1/bins/{id}/move", h.MoveBin)
	mux.HandleFunc("POST /api/v1/items/{id}/return-requests", h.RequestReturn)
	mux.HandleFunc("POST /api/v1/items/{id}/return-requests/{requestID}/cancel", h.CancelReturnRequest)
}

// addToBinRequest is POST /api/v1/items/{id}/add-to-bin's request body. No
// note field: per NSTR-41 R16, a note is check-out/return only.
type addToBinRequest struct {
	BinID string `json:"bin_id"`
}

// checkOutRequest is POST /api/v1/items/{id}/check-out's request body.
type checkOutRequest struct {
	Note *string `json:"note"`
}

// returnToBinRequest is POST /api/v1/items/{id}/return's request body.
type returnToBinRequest struct {
	BinID string  `json:"bin_id"`
	Note  *string `json:"note"`
}

// moveBinRequest is POST /api/v1/bins/{id}/move's request body.
type moveBinRequest struct {
	LocationID string `json:"location_id"`
}

// requestReturnRequest is POST /api/v1/items/{id}/return-requests' request
// body.
type requestReturnRequest struct {
	Message *string `json:"message"`
}

// binMoveResponse is POST /api/v1/bins/{id}/move's response: the same facts
// app.MoveResult carries, minus MovedBy (the caller already knows who it
// is — the request's own bearer principal). MovedAt is nil on an
// idempotent retry (AC 4): ErrBinAlreadyInLocation means no move actually
// happened this request, so there is no fresh timestamp to report, and a
// stale or synthesized one would misrepresent that.
type binMoveResponse struct {
	BinID          string     `json:"bin_id"`
	FromLocationID string     `json:"from_location_id"`
	ToLocationID   string     `json:"to_location_id"`
	MovedAt        *time.Time `json:"moved_at"`
}

// returnRequestResponse is every return-request endpoint's response shape.
type returnRequestResponse struct {
	ID          string     `json:"id"`
	ItemID      string     `json:"item_id"`
	RequesterID string     `json:"requester_id"`
	HolderID    string     `json:"holder_id"`
	Message     *string    `json:"message"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	ResolvedAt  *time.Time `json:"resolved_at"`
}

func returnRequestResponseFrom(rr domain.ReturnRequest) returnRequestResponse {
	return returnRequestResponse{
		ID: rr.ID.String(), ItemID: rr.ItemID.String(),
		RequesterID: rr.RequesterID.String(), HolderID: rr.HolderID.String(),
		Message: rr.Message, Status: rr.Status.String(),
		CreatedAt: rr.CreatedAt, ResolvedAt: rr.ResolvedAt,
	}
}

// AddToBin handles POST /api/v1/items/{id}/add-to-bin, delegating to
// OperationService.AddToBin. A retried request (ErrItemAlreadyInBin)
// answers 200 with the item's current state when it is already sitting in
// the requested bin, or 409 item_already_in_bin when it is not (AC 4).
func (h *OperationsAPIHandlers) AddToBin(w http.ResponseWriter, r *http.Request) {
	itemID, ok := parsePathID(w, r, h.logger, "id", domain.ParseItemID)
	if !ok {
		return
	}
	var req addToBinRequest
	if !decodeJSONBody(w, r, h.logger, &req) {
		return
	}
	binID, err := domain.ParseBinID(req.BinID)
	if err != nil {
		writeMalformedField(w, h.logger, "bin_id")
		return
	}

	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	op, err := h.operations.AddToBin(r.Context(), viewer, itemID, binID)
	if err != nil {
		if errors.Is(err, domain.ErrItemAlreadyInBin) {
			h.respondIfCurrentBin(w, r, viewer, itemID, binID, api.CodeItemAlreadyInBin, "item is already in a different bin")
			return
		}
		writeOperationError(w, r, h.logger, err, "operations api: add to bin")
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, itemResponseFrom(*op.Item))
}

// CheckOut handles POST /api/v1/items/{id}/check-out, delegating to
// OperationService.RemoveFromBin (the ticket's own "remove from bin"). A
// retried request (ErrItemAlreadyCheckedOut) answers 200 with the item's
// current state when it is already held by the requesting principal, or
// 409 item_already_checked_out when a different holder has it (AC 4).
func (h *OperationsAPIHandlers) CheckOut(w http.ResponseWriter, r *http.Request) {
	itemID, ok := parsePathID(w, r, h.logger, "id", domain.ParseItemID)
	if !ok {
		return
	}
	var req checkOutRequest
	if !decodeJSONBody(w, r, h.logger, &req) {
		return
	}

	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	op, err := h.operations.RemoveFromBin(r.Context(), viewer, itemID, req.Note)
	if err != nil {
		if errors.Is(err, domain.ErrItemAlreadyCheckedOut) {
			h.respondIfHeldBy(w, r, viewer, itemID, viewer.UserID)
			return
		}
		writeOperationError(w, r, h.logger, err, "operations api: check out")
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, itemResponseFrom(*op.Item))
}

// Return handles POST /api/v1/items/{id}/return, delegating to
// OperationService.ReturnToBin. A retried request (ErrItemNotCheckedOut)
// answers 200 with the item's current state when it is already sitting in
// the requested bin, or 409 item_not_checked_out otherwise (AC 4) — the
// same comparison AddToBin's own retry makes, since both ask "is the item
// already where this request wants it".
func (h *OperationsAPIHandlers) Return(w http.ResponseWriter, r *http.Request) {
	itemID, ok := parsePathID(w, r, h.logger, "id", domain.ParseItemID)
	if !ok {
		return
	}
	var req returnToBinRequest
	if !decodeJSONBody(w, r, h.logger, &req) {
		return
	}
	binID, err := domain.ParseBinID(req.BinID)
	if err != nil {
		writeMalformedField(w, h.logger, "bin_id")
		return
	}

	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	op, err := h.operations.ReturnToBin(r.Context(), viewer, itemID, binID, req.Note)
	if err != nil {
		if errors.Is(err, domain.ErrItemNotCheckedOut) {
			h.respondIfCurrentBin(w, r, viewer, itemID, binID, api.CodeItemNotCheckedOut, "item is not checked out")
			return
		}
		writeOperationError(w, r, h.logger, err, "operations api: return")
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, itemResponseFrom(*op.Item))
}

// respondIfCurrentBin re-reads itemID's visibility-scoped detail (the
// idempotency re-read AddToBin/Return's own retry needs) and answers 200
// with its current state when it is already sitting in wantBinID, or the
// given conflict code/message otherwise. Shared by AddToBin and Return: both
// guards (ErrItemAlreadyInBin, ErrItemNotCheckedOut) resolve the identical
// question — is the item already in the bin this request named.
func (h *OperationsAPIHandlers) respondIfCurrentBin(w http.ResponseWriter, r *http.Request, viewer identity.Principal, itemID domain.ItemID, wantBinID domain.BinID, code api.Code, message string) {
	result, err := h.detail.Detail(r.Context(), viewer, itemID)
	if err != nil {
		writeOperationError(w, r, h.logger, err, "operations api: idempotent re-read")
		return
	}
	if result.Item.CurrentBinID != nil && *result.Item.CurrentBinID == wantBinID {
		api.WriteJSON(w, h.logger, http.StatusOK, itemResponseFrom(result.Item))
		return
	}
	api.WriteError(w, h.logger, http.StatusConflict, code, message)
}

// respondIfHeldBy re-reads itemID's visibility-scoped detail and answers
// 200 with its current state when it is already held by wantHolder, or 409
// item_already_checked_out otherwise — CheckOut's own idempotency re-read.
func (h *OperationsAPIHandlers) respondIfHeldBy(w http.ResponseWriter, r *http.Request, viewer identity.Principal, itemID domain.ItemID, wantHolder identity.UserID) {
	result, err := h.detail.Detail(r.Context(), viewer, itemID)
	if err != nil {
		writeOperationError(w, r, h.logger, err, "operations api: idempotent re-read")
		return
	}
	if result.Item.HeldBy != nil && *result.Item.HeldBy == wantHolder {
		api.WriteJSON(w, h.logger, http.StatusOK, itemResponseFrom(result.Item))
		return
	}
	api.WriteError(w, h.logger, http.StatusConflict, api.CodeItemAlreadyCheckedOut, "item is already checked out to someone else")
}

// MoveBin handles POST /api/v1/bins/{id}/move, delegating to
// BinMover.Move. Per Move's own doc, ErrBinAlreadyInLocation fires only
// when target already equals the bin's current location — always the
// retried case (AC 4), never a genuine conflict — so it always answers 200,
// with MovedAt left nil since no move happened this request.
func (h *OperationsAPIHandlers) MoveBin(w http.ResponseWriter, r *http.Request) {
	binID, ok := parsePathID(w, r, h.logger, "id", domain.ParseBinID)
	if !ok {
		return
	}
	var req moveBinRequest
	if !decodeJSONBody(w, r, h.logger, &req) {
		return
	}
	locationID, err := domain.ParseLocationID(req.LocationID)
	if err != nil {
		writeMalformedField(w, h.logger, "location_id")
		return
	}

	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	result, err := h.mover.Move(r.Context(), viewer, binID, locationID)
	if err != nil {
		if errors.Is(err, domain.ErrBinAlreadyInLocation) {
			api.WriteJSON(w, h.logger, http.StatusOK, binMoveResponse{
				BinID: binID.String(), FromLocationID: locationID.String(), ToLocationID: locationID.String(),
			})
			return
		}
		writeOperationError(w, r, h.logger, err, "operations api: move bin")
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, binMoveResponse{
		BinID: result.BinID.String(), FromLocationID: result.FromLocationID.String(), ToLocationID: result.ToLocationID.String(),
		MovedAt: &result.MovedAt,
	})
}

// RequestReturn handles POST /api/v1/items/{id}/return-requests, delegating
// to ReturnRequestService.Request. A retried request (ErrDuplicateReturnRequest)
// always belongs to the SAME requester — the uniqueness guard is scoped to
// (item_id, requester_id) while open — so it answers 200 with the caller's
// own existing open request (AC 4) rather than a conflict.
func (h *OperationsAPIHandlers) RequestReturn(w http.ResponseWriter, r *http.Request) {
	itemID, ok := parsePathID(w, r, h.logger, "id", domain.ParseItemID)
	if !ok {
		return
	}
	var req requestReturnRequest
	if !decodeJSONBody(w, r, h.logger, &req) {
		return
	}

	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	created, err := h.returnRequests.Request(r.Context(), viewer, itemID, req.Message)
	if err != nil {
		if errors.Is(err, domain.ErrDuplicateReturnRequest) {
			h.respondWithOpenRequest(w, r, viewer, itemID)
			return
		}
		writeOperationError(w, r, h.logger, err, "operations api: request return")
		return
	}
	api.WriteJSON(w, h.logger, http.StatusCreated, returnRequestResponseFrom(*created))
}

// respondWithOpenRequest answers 200 with viewer's own existing open return
// request on itemID — RequestReturn's own idempotent-retry re-read.
func (h *OperationsAPIHandlers) respondWithOpenRequest(w http.ResponseWriter, r *http.Request, viewer identity.Principal, itemID domain.ItemID) {
	requests, err := h.returnRequests.ListForItem(r.Context(), viewer, itemID)
	if err != nil {
		writeOperationError(w, r, h.logger, err, "operations api: idempotent re-read: list return requests")
		return
	}
	for _, req := range requests {
		if req.Status == domain.ReturnRequestStatusOpen && req.RequesterID == viewer.UserID {
			api.WriteJSON(w, h.logger, http.StatusOK, returnRequestResponseFrom(req))
			return
		}
	}
	// Defensive: the duplicate guard just fired inside Request's own
	// transaction, so an open request for viewer must exist; this is
	// reachable only if a concurrent cancel/fulfil won a race after that
	// guard ran and before this re-read.
	api.WriteError(w, h.logger, http.StatusConflict, api.CodeConflict, "an open return request already exists")
}

// CancelReturnRequest handles POST
// /api/v1/items/{id}/return-requests/{requestID}/cancel, delegating to
// ReturnRequestService.Cancel. Cancel itself returns no updated row, so
// both a fresh cancel and a retried one (ErrReturnRequestNotOpen) fall
// through to the same re-read below, which is what reports the 200 body —
// the one difference is that a fulfilled request (the OTHER terminal state
// ErrReturnRequestNotOpen can mean) is a genuine conflict, not a retry (AC 4).
func (h *OperationsAPIHandlers) CancelReturnRequest(w http.ResponseWriter, r *http.Request) {
	itemID, ok := parsePathID(w, r, h.logger, "id", domain.ParseItemID)
	if !ok {
		return
	}
	requestID, ok := parsePathID(w, r, h.logger, "requestID", domain.ParseReturnRequestID)
	if !ok {
		return
	}

	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	err := h.returnRequests.Cancel(r.Context(), viewer, itemID, requestID)
	if err != nil && !errors.Is(err, domain.ErrReturnRequestNotOpen) {
		writeOperationError(w, r, h.logger, err, "operations api: cancel return request")
		return
	}
	h.respondCancelledOrConflict(w, r, viewer, itemID, requestID)
}

// respondCancelledOrConflict re-reads itemID's return requests and answers
// 200 with requestID's own state when it is cancelled (a fresh cancel this
// call just performed, or a retried one — Cancel's success carries no row,
// so both share this one path), 409 return_request_not_open when it is
// instead fulfilled, or 404 return_request_not_found on the (racing-delete)
// case where it no longer appears at all.
func (h *OperationsAPIHandlers) respondCancelledOrConflict(w http.ResponseWriter, r *http.Request, viewer identity.Principal, itemID domain.ItemID, requestID domain.ReturnRequestID) {
	requests, err := h.returnRequests.ListForItem(r.Context(), viewer, itemID)
	if err != nil {
		writeOperationError(w, r, h.logger, err, "operations api: cancel return request: reload")
		return
	}
	for _, req := range requests {
		if req.ID != requestID {
			continue
		}
		if req.Status == domain.ReturnRequestStatusCancelled {
			api.WriteJSON(w, h.logger, http.StatusOK, returnRequestResponseFrom(req))
			return
		}
		api.WriteError(w, h.logger, http.StatusConflict, api.CodeReturnRequestNotOpen, "return request has already been resolved")
		return
	}
	api.WriteError(w, h.logger, http.StatusNotFound, api.CodeReturnRequestNotFound, "return request not found")
}
