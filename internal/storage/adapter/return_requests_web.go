package adapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
	"github.com/ericfisherdev/nestorage/web/components"
)

// returnRequestOperator is the narrow port (ISP) ItemsWebHandlers depends on
// for the item detail page's return-request control (NSTR-43), satisfied by
// *app.ReturnRequestService. Every method mirrors that service's own
// visibility-scoped shape: an unknown/invisible itemID surfaces as
// domain.ErrItemNotFound before a request is ever read or mutated.
type returnRequestOperator interface {
	Request(ctx context.Context, actor identity.Principal, itemID domain.ItemID, message *string) (*domain.ReturnRequest, error)
	Cancel(ctx context.Context, actor identity.Principal, itemID domain.ItemID, requestID domain.ReturnRequestID) error
	ListForItem(ctx context.Context, viewer identity.Principal, itemID domain.ItemID) ([]domain.ReturnRequest, error)
}

// RequestReturn handles POST /items/{id}/return-requests: CSRF,
// ReturnRequestService.Request with the form's optional message field, then
// re-render the WHOLE item detail fragment either way — mirroring
// CheckOut/Return exactly (never a section-only swap like AddLink's own),
// since a request/cancel changes what the detail page's placement panel
// itself shows (the request form vs. the cancel control).
func (h *ItemsWebHandlers) RequestReturn(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	if _, err := h.returnRequests.Request(r.Context(), viewer, id, formValuePtr(r, "message")); err != nil {
		status, msg, mapped := mapReturnRequestError(err)
		if !mapped {
			h.logger.ErrorContext(r.Context(), "items: return requests: request", "error", err)
			http.Error(w, errInternalServerError, http.StatusInternalServerError)
			return
		}
		h.renderDetail(w, r, viewer, id, status, msg)
		return
	}
	h.renderDetail(w, r, viewer, id, http.StatusOK, "")
}

// CancelReturnRequest handles POST /items/{id}/return-requests/{requestID}/cancel:
// CSRF, ReturnRequestService.Cancel, then re-render the whole item detail
// fragment either way — see RequestReturn's own doc.
func (h *ItemsWebHandlers) CancelReturnRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	requestID, ok := h.pathReturnRequestID(w, r)
	if !ok {
		return
	}
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	if err := h.returnRequests.Cancel(r.Context(), viewer, id, requestID); err != nil {
		status, msg, mapped := mapReturnRequestError(err)
		if !mapped {
			h.logger.ErrorContext(r.Context(), "items: return requests: cancel", "error", err)
			http.Error(w, errInternalServerError, http.StatusInternalServerError)
			return
		}
		h.renderDetail(w, r, viewer, id, status, msg)
		return
	}
	h.renderDetail(w, r, viewer, id, http.StatusOK, "")
}

// pathReturnRequestID parses the {requestID} path value, answering 400 and
// reporting ok=false on a malformed one — mirrors pathItemLinkID's own
// shape.
func (h *ItemsWebHandlers) pathReturnRequestID(w http.ResponseWriter, r *http.Request) (domain.ReturnRequestID, bool) {
	id, err := domain.ParseReturnRequestID(r.PathValue("requestID"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return domain.ReturnRequestID{}, false
	}
	return id, true
}

// mapReturnRequestError maps a domain error from a ReturnRequestService call
// to the HTTP status and inline message the detail fragment re-renders
// with (the same shared FormError banner CheckOut/Return already use — see
// RequestReturn's own doc for why this ticket does not add a second,
// section-local error channel). ok reports whether err was recognized; an
// unrecognized error is the caller's cue to log it and answer a generic 500
// instead — mirrors mapItemOperationError/mapItemLinkError's own shape.
func mapReturnRequestError(err error) (status int, message string, ok bool) {
	switch {
	case errors.Is(err, domain.ErrInvalidReturnRequestMessage):
		return http.StatusUnprocessableEntity, fmt.Sprintf("Message must be %d characters or fewer.", domain.MaxReturnRequestMessageRunes), true
	case errors.Is(err, domain.ErrHolderRequired):
		return http.StatusForbidden, "Only a signed-in person can request a return.", true
	case errors.Is(err, domain.ErrRequesterHoldsItem):
		return http.StatusConflict, "You already hold this item.", true
	case errors.Is(err, domain.ErrDuplicateReturnRequest):
		return http.StatusConflict, "You already have an open request for this item.", true
	case errors.Is(err, domain.ErrItemNotCheckedOut):
		return http.StatusConflict, "This item is not checked out.", true
	case errors.Is(err, domain.ErrReturnRequestNotFound):
		return http.StatusNotFound, "That request no longer exists.", true
	case errors.Is(err, domain.ErrReturnRequestNotOpen):
		return http.StatusConflict, "That request has already been resolved.", true
	case errors.Is(err, domain.ErrItemNotFound):
		return http.StatusNotFound, "That item no longer exists.", true
	default:
		return 0, "", false
	}
}

// formValuePtr returns r's already-parsed form value name as *string: nil
// when absent or blank, a pointer to the trimmed value otherwise — the
// optional-message contract domain.ValidateReturnRequestMessage expects
// (nil means "no message supplied", not "supplied but blank", which it
// rejects). r.ParseForm has already run by the time any handler here calls
// this (verifyRequest's own precondition).
func formValuePtr(r *http.Request, name string) *string {
	v := strings.TrimSpace(r.FormValue(name))
	if v == "" {
		return nil
	}
	return &v
}

// buildReturnRequestSectionView projects item/viewer/requests into
// ReturnRequestSection's view model: CanRequest is true exactly when the
// item is checked out, viewer is not its current holder, and viewer has no
// open request on it already; OpenRequestID is viewer's own open request's
// id when they have one (the cancel control's target). The two are
// mutually exclusive by construction — a viewer with an open request is
// never also offered a fresh request form.
func buildReturnRequestSectionView(item domain.Item, viewer identity.Principal, requests []domain.ReturnRequest, csrfToken string) components.ReturnRequestSectionView {
	isHolder := item.HeldBy != nil && *item.HeldBy == viewer.UserID
	var openID string
	for _, req := range requests {
		if req.Status == domain.ReturnRequestStatusOpen && req.RequesterID == viewer.UserID {
			openID = req.ID.String()
			break
		}
	}
	return components.ReturnRequestSectionView{
		ItemID: item.ID.String(), CSRFToken: csrfToken,
		CanRequest:    item.State() == domain.StateCheckedOut && !isHolder && openID == "",
		OpenRequestID: openID,
	}
}
