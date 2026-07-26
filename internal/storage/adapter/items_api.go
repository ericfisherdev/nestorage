package adapter

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/api"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// itemAPIService is the narrow port (ISP) ItemsAPIHandlers depends on,
// satisfied by *app.ItemService. NSTR-41 built Create/Edit/Delete with no
// production caller, noting Sprint 8's public API as the one to eventually
// reach them (see app.ItemService.Create's own doc) — this is that caller.
type itemAPIService interface {
	Get(ctx context.Context, viewer identity.Principal, id domain.ItemID) (*domain.Item, error)
	ListVisible(ctx context.Context, viewer identity.Principal, filter domain.ItemFilter) ([]domain.Item, error)
	Create(ctx context.Context, name string, description *string, quantity int, binID domain.BinID, actor identity.Principal) (*domain.Item, error)
	Edit(ctx context.Context, id domain.ItemID, name string, description *string, quantity int, actor identity.Principal) error
	Delete(ctx context.Context, id domain.ItemID, actor identity.Principal) error
}

// ItemsAPIHandlers serves NSTR-54's public JSON CRUD surface for items,
// delegating to *app.ItemService — distinct from the *app.ItemQueryService/
// *app.OperationService pair ItemsWebHandlers consumes for the search/detail
// screens (item_query.go), since this ticket's PUT/DELETE routes are plain
// edit/delete, not a placement change (NSTR-55's own operations endpoints).
// Every route is registered on the api.Router NSTR-53 mounts behind its
// bearer-only gate; this type carries no session and no CSRF dependency.
type ItemsAPIHandlers struct {
	items  itemAPIService
	logger *slog.Logger
}

// NewItemsAPIHandlers constructs ItemsAPIHandlers. Both dependencies are
// required; a missing one panics at construction time, matching every other
// WebHandlers/APIHandlers constructor in this codebase.
func NewItemsAPIHandlers(items itemAPIService, logger *slog.Logger) *ItemsAPIHandlers {
	if items == nil {
		panic("storage/adapter: NewItemsAPIHandlers requires a non-nil itemAPIService")
	}
	if logger == nil {
		panic("storage/adapter: NewItemsAPIHandlers requires a non-nil logger")
	}
	return &ItemsAPIHandlers{items: items, logger: logger}
}

// Routes registers the item CRUD routes on mux — an api.Registrar (DIP), see
// LocationsAPIHandlers.Routes' own doc for why.
func (h *ItemsAPIHandlers) Routes(mux api.Registrar) {
	mux.HandleFunc("GET /api/v1/items", h.List)
	mux.HandleFunc("POST /api/v1/items", h.Create)
	mux.HandleFunc("GET /api/v1/items/{id}", h.Get)
	mux.HandleFunc("PUT /api/v1/items/{id}", h.Update)
	mux.HandleFunc("DELETE /api/v1/items/{id}", h.Delete)
}

// itemResponse is every item endpoint's response shape.
type itemResponse struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	Description        *string   `json:"description"`
	Quantity           int       `json:"quantity"`
	State              string    `json:"state"`
	BinID              *string   `json:"bin_id"`
	HeldBy             *string   `json:"held_by"`
	PlacementChangedAt time.Time `json:"placement_changed_at"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// itemsListResponse is GET /api/v1/items' response envelope.
type itemsListResponse struct {
	Items      []itemResponse `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}

// itemCreateRequest is POST /api/v1/items' request body: an item is always
// created sitting in a bin through this route — check-out is NSTR-55's own
// operations endpoint, not this one's.
type itemCreateRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Quantity    int     `json:"quantity"`
	BinID       string  `json:"bin_id"`
}

// itemUpdateRequest is PUT /api/v1/items/{id}'s request body: name,
// description, and quantity — the exact fields ItemService.Edit exposes.
// Placement never appears here (see ItemsAPIHandlers.Update's own doc).
type itemUpdateRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Quantity    int     `json:"quantity"`
}

// List handles GET /api/v1/items: every item the caller may see, paginated
// by name, optionally narrowed by ?bin={id} and/or ?holder={user id}
// (AND-ed when both are given — domain.ItemFilter's own doc).
func (h *ItemsAPIHandlers) List(w http.ResponseWriter, r *http.Request) {
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	filter, badField, ok := parseItemFilter(r)
	if !ok {
		api.WriteError(w, h.logger, http.StatusBadRequest, api.CodeInvalidRequest, "malformed request",
			api.FieldDetail{Field: badField, Message: "malformed id"})
		return
	}

	items, err := h.items.ListVisible(r.Context(), viewer, filter)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "items api: list", "error", err)
		api.WriteError(w, h.logger, http.StatusInternalServerError, api.CodeInternal, errInternalServerError)
		return
	}

	window, next, err := page(items, requestCursor(r), requestLimit(r), itemCursorKey)
	if err != nil {
		writeMalformedCursor(w, h.logger)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, itemsListResponse{Items: itemResponses(window), NextCursor: cursorOrNil(next)})
}

// Get handles GET /api/v1/items/{id}.
func (h *ItemsAPIHandlers) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePathID(w, r, h.logger, "id", domain.ParseItemID)
	if !ok {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	it, err := h.items.Get(r.Context(), viewer, id)
	if err != nil {
		writeDomainError(w, r, h.logger, err, "items api: get")
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, itemResponseFrom(*it))
}

// Create handles POST /api/v1/items: 403 for an integration principal
// (requireUserPrincipal), then delegate to ItemService.Create.
func (h *ItemsAPIHandlers) Create(w http.ResponseWriter, r *http.Request) {
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	if !requireUserPrincipal(w, h.logger, viewer) {
		return
	}

	var req itemCreateRequest
	if !decodeJSONBody(w, r, h.logger, &req) {
		return
	}
	binID, err := domain.ParseBinID(req.BinID)
	if err != nil {
		writeMalformedField(w, h.logger, "bin_id")
		return
	}

	it, err := h.items.Create(r.Context(), req.Name, req.Description, req.Quantity, binID, viewer)
	if err != nil {
		writeDomainError(w, r, h.logger, err, "items api: create")
		return
	}
	api.WriteJSON(w, h.logger, http.StatusCreated, itemResponseFrom(*it))
}

// Update handles PUT /api/v1/items/{id}: a visibility-scoped Get first —
// ItemService.Edit is deliberately not itself visibility-scoped (its own
// doc), so this preceding read is what authorizes the request, the same
// authorize-by-read contract Delete below follows.
func (h *ItemsAPIHandlers) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePathID(w, r, h.logger, "id", domain.ParseItemID)
	if !ok {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	if _, err := h.items.Get(r.Context(), viewer, id); err != nil {
		writeDomainError(w, r, h.logger, err, "items api: update")
		return
	}

	var req itemUpdateRequest
	if !decodeJSONBody(w, r, h.logger, &req) {
		return
	}
	if err := h.items.Edit(r.Context(), id, req.Name, req.Description, req.Quantity, viewer); err != nil {
		writeDomainError(w, r, h.logger, err, "items api: update")
		return
	}

	it, err := h.items.Get(r.Context(), viewer, id)
	if err != nil {
		writeDomainError(w, r, h.logger, err, "items api: update: reload")
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, itemResponseFrom(*it))
}

// Delete handles DELETE /api/v1/items/{id}: a visibility-scoped Get first,
// then ItemService.Delete — the same authorize-by-read contract Update
// follows, straight from Delete's own doc.
func (h *ItemsAPIHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePathID(w, r, h.logger, "id", domain.ParseItemID)
	if !ok {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	if _, err := h.items.Get(r.Context(), viewer, id); err != nil {
		writeDomainError(w, r, h.logger, err, "items api: delete")
		return
	}
	if err := h.items.Delete(r.Context(), id, viewer); err != nil {
		writeDomainError(w, r, h.logger, err, "items api: delete")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseItemFilter builds List's own domain.ItemFilter from the ?bin= and
// ?holder= query parameters, both optional. Reports ok=false and the
// offending parameter's name on a malformed (not merely absent) value.
func parseItemFilter(r *http.Request) (filter domain.ItemFilter, badField string, ok bool) {
	if binStr := r.URL.Query().Get("bin"); binStr != "" {
		id, err := domain.ParseBinID(binStr)
		if err != nil {
			return domain.ItemFilter{}, "bin", false
		}
		filter.BinID = &id
	}
	if holderStr := r.URL.Query().Get("holder"); holderStr != "" {
		id, err := identity.ParseUserID(holderStr)
		if err != nil {
			return domain.ItemFilter{}, "holder", false
		}
		filter.HeldBy = &id
	}
	return filter, "", true
}

// itemCursorKey is List's own cursorKeyFunc: name-then-id, the exact order
// domain.ItemRepository.ListVisible returns (its own ORDER BY).
func itemCursorKey(it domain.Item) (key, id string) {
	return it.Name, it.ID.String()
}

// itemResponseFrom projects a domain.Item into its response shape — State
// renders as "in_bin"/"checked_out" (domain.PlacementState's own string
// values), never the bare boolean fields it is derived from.
func itemResponseFrom(it domain.Item) itemResponse {
	return itemResponse{
		ID:                 it.ID.String(),
		Name:               it.Name,
		Description:        it.Description,
		Quantity:           it.Quantity,
		State:              it.State().String(),
		BinID:              optionalIDString(it.CurrentBinID),
		HeldBy:             optionalIDString(it.HeldBy),
		PlacementChangedAt: it.PlacementChangedAt,
		CreatedAt:          it.CreatedAt,
		UpdatedAt:          it.UpdatedAt,
	}
}

// itemResponses projects List's own read model into the response's per-row
// shape.
func itemResponses(items []domain.Item) []itemResponse {
	out := make([]itemResponse, 0, len(items))
	for _, it := range items {
		out = append(out, itemResponseFrom(it))
	}
	return out
}
