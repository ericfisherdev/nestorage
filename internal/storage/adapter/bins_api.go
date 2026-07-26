package adapter

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/api"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// binAPIService is the narrow port (ISP) BinsAPIHandlers depends on,
// satisfied by *app.BinService. It overlaps binQueryCommandService
// (bins_web.go) but is its own declaration rather than a reuse of it: this
// handler additionally needs ListVisibleByLocation (the ?location= filter)
// and never needs GetByCode or Move, so a fresh, exactly-sized interface is
// the honest ISP choice over widening (or narrowing) an existing one scoped
// to a different consumer.
type binAPIService interface {
	ListVisible(ctx context.Context, viewer identity.Principal) ([]app.BinView, error)
	ListVisibleByLocation(ctx context.Context, viewer identity.Principal, locationID domain.LocationID) ([]app.BinView, error)
	GetByID(ctx context.Context, viewer identity.Principal, id domain.BinID) (*app.BinView, error)
	Create(ctx context.Context, input app.CreateBinInput) (*domain.Bin, error)
	Edit(ctx context.Context, viewer identity.Principal, id domain.BinID, name, description string, ownerID *identity.UserID, visibility domain.Visibility) error
	Delete(ctx context.Context, viewer identity.Principal, id domain.BinID) error
}

// BinsAPIHandlers serves NSTR-54's public JSON CRUD surface for bins,
// delegating to the same *app.BinService methods BinsWebHandlers already
// consumes (bins_web.go) — no rule is duplicated between the HTML and JSON
// surfaces. Every route is registered on the api.Router NSTR-53 mounts
// behind its bearer-only gate; this type carries no session and no CSRF
// dependency, unlike its web counterpart.
type BinsAPIHandlers struct {
	bins   binAPIService
	logger *slog.Logger
}

// NewBinsAPIHandlers constructs BinsAPIHandlers. Both dependencies are
// required; a missing one panics at construction time, matching every other
// WebHandlers/APIHandlers constructor in this codebase.
func NewBinsAPIHandlers(bins binAPIService, logger *slog.Logger) *BinsAPIHandlers {
	if bins == nil {
		panic("storage/adapter: NewBinsAPIHandlers requires a non-nil binAPIService")
	}
	if logger == nil {
		panic("storage/adapter: NewBinsAPIHandlers requires a non-nil logger")
	}
	return &BinsAPIHandlers{bins: bins, logger: logger}
}

// Routes registers the bin CRUD routes on mux — an api.Registrar (DIP), see
// LocationsAPIHandlers.Routes' own doc for why.
func (h *BinsAPIHandlers) Routes(mux api.Registrar) {
	mux.HandleFunc("GET /api/v1/bins", h.List)
	mux.HandleFunc("POST /api/v1/bins", h.Create)
	mux.HandleFunc("GET /api/v1/bins/{id}", h.Get)
	mux.HandleFunc("PUT /api/v1/bins/{id}", h.Update)
	mux.HandleFunc("DELETE /api/v1/bins/{id}", h.Delete)
}

// binResponse is every bin endpoint's response shape — List, Get, Create,
// and Update all return this exact projection of an app.BinView (Create
// synthesizes ItemCount as 0: a bin the request itself just created cannot
// already hold a visible item).
type binResponse struct {
	ID          string    `json:"id"`
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	LocationID  string    `json:"location_id"`
	OwnerID     *string   `json:"owner_id"`
	Visibility  string    `json:"visibility"`
	ItemCount   int       `json:"item_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// binsListResponse is GET /api/v1/bins' response envelope.
type binsListResponse struct {
	Bins       []binResponse `json:"bins"`
	NextCursor *string       `json:"next_cursor"`
}

// binCreateRequest is POST /api/v1/bins' request body. Visibility is
// optional — an empty value defaults to domain.VisibilityPublic, matching
// "bins default to public" (BinRepository.Create's own doc).
type binCreateRequest struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	LocationID  string  `json:"location_id"`
	OwnerID     *string `json:"owner_id"`
	Visibility  string  `json:"visibility"`
}

// binUpdateRequest is PUT /api/v1/bins/{id}'s request body: full replacement
// of every field BinService.Edit exposes — code and location are immutable
// through this route (BinRepository.Update's own doc), so neither appears
// here.
type binUpdateRequest struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	OwnerID     *string `json:"owner_id"`
	Visibility  string  `json:"visibility"`
}

// List handles GET /api/v1/bins: every bin the caller may see, paginated by
// code, optionally narrowed to one location via ?location={id}.
func (h *BinsAPIHandlers) List(w http.ResponseWriter, r *http.Request) {
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	views, ok := h.loadBinViews(w, r, viewer)
	if !ok {
		return
	}

	window, next, err := page(views, requestCursor(r), requestLimit(r), binViewCursorKey)
	if err != nil {
		writeMalformedCursor(w, h.logger)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, binsListResponse{Bins: binResponses(window), NextCursor: cursorOrNil(next)})
}

// loadBinViews loads List's underlying read: ListVisibleByLocation when
// ?location= is present, ListVisible otherwise. Reports ok=false having
// already written the response on a malformed location id or a service
// failure.
func (h *BinsAPIHandlers) loadBinViews(w http.ResponseWriter, r *http.Request, viewer identity.Principal) ([]app.BinView, bool) {
	locationParam := r.URL.Query().Get("location")
	if locationParam == "" {
		views, err := h.bins.ListVisible(r.Context(), viewer)
		if err != nil {
			h.logAndWrite500(w, r, "bins api: list", err)
			return nil, false
		}
		return views, true
	}

	locationID, err := domain.ParseLocationID(locationParam)
	if err != nil {
		api.WriteError(w, h.logger, http.StatusBadRequest, api.CodeInvalidRequest, "malformed request",
			api.FieldDetail{Field: "location", Message: "malformed id"})
		return nil, false
	}
	views, err := h.bins.ListVisibleByLocation(r.Context(), viewer, locationID)
	if err != nil {
		h.logAndWrite500(w, r, "bins api: list by location", err)
		return nil, false
	}
	return views, true
}

// Create handles POST /api/v1/bins: 403 for an integration principal
// (requireUserPrincipal), pre-validated code/name (BinService.Edit/Create
// wrap both under the single domain.ErrInvalidBin sentinel — pre-validating
// here is what lets this handler answer a precise field, rather than
// mapDomainError's own defensive, field-less ErrInvalidBin fallback), then
// delegate to BinService.Create.
func (h *BinsAPIHandlers) Create(w http.ResponseWriter, r *http.Request) {
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	if !requireUserPrincipal(w, h.logger, viewer) {
		return
	}

	var req binCreateRequest
	if !decodeJSONBody(w, r, h.logger, &req) {
		return
	}
	if err := domain.ValidateBinCode(req.Code); err != nil {
		writeInvalidField(w, h.logger, "code", "must not be blank and at most 32 characters")
		return
	}
	if err := domain.ValidateBinName(req.Name); err != nil {
		writeInvalidField(w, h.logger, "name", "must not be blank")
		return
	}
	locationID, err := domain.ParseLocationID(req.LocationID)
	if err != nil {
		writeMalformedField(w, h.logger, "location_id")
		return
	}
	ownerID, err := parseOptionalID(req.OwnerID, identity.ParseUserID)
	if err != nil {
		writeMalformedField(w, h.logger, "owner_id")
		return
	}
	visibility, ok := h.parseCreateVisibility(w, req.Visibility)
	if !ok {
		return
	}

	b, err := h.bins.Create(r.Context(), app.CreateBinInput{
		Code:        domain.NormalizeBinCode(req.Code),
		Name:        req.Name,
		Description: req.Description,
		LocationID:  locationID,
		OwnerID:     ownerID,
		Visibility:  visibility,
		CreatedBy:   viewer.UserID,
	})
	if err != nil {
		writeDomainError(w, r, h.logger, err, "bins api: create")
		return
	}
	api.WriteJSON(w, h.logger, http.StatusCreated, binResponseFrom(*b, 0))
}

// parseCreateVisibility defaults an empty Visibility to public (matching
// "bins default to public"), or parses the supplied one, writing a 422 and
// reporting ok=false on an unrecognized value.
func (h *BinsAPIHandlers) parseCreateVisibility(w http.ResponseWriter, raw string) (domain.Visibility, bool) {
	if raw == "" {
		return domain.VisibilityPublic, true
	}
	v, err := domain.ParseVisibility(raw)
	if err != nil {
		writeInvalidField(w, h.logger, "visibility", `must be "public" or "private"`)
		return "", false
	}
	return v, true
}

// Get handles GET /api/v1/bins/{id}: a non-owner's private bin 404s here,
// never 403s, per BinService.GetByID's own visibility contract.
func (h *BinsAPIHandlers) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePathID(w, r, h.logger, "id", domain.ParseBinID)
	if !ok {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	view, err := h.bins.GetByID(r.Context(), viewer, id)
	if err != nil {
		writeDomainError(w, r, h.logger, err, "bins api: get")
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, binResponseFrom(view.Bin, view.ItemCount))
}

// Update handles PUT /api/v1/bins/{id}: full replacement of name,
// description, owner_id, and visibility — BinService.Edit overwrites all
// four, so PUT (not PATCH) is the honest verb (see binUpdateRequest's own
// doc). name is pre-validated here for the same reason Create's is:
// BinService.Edit does not itself validate a blank name before writing it.
func (h *BinsAPIHandlers) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePathID(w, r, h.logger, "id", domain.ParseBinID)
	if !ok {
		return
	}
	var req binUpdateRequest
	if !decodeJSONBody(w, r, h.logger, &req) {
		return
	}
	if err := domain.ValidateBinName(req.Name); err != nil {
		writeInvalidField(w, h.logger, "name", "must not be blank")
		return
	}
	ownerID, err := parseOptionalID(req.OwnerID, identity.ParseUserID)
	if err != nil {
		writeMalformedField(w, h.logger, "owner_id")
		return
	}
	visibility, err := domain.ParseVisibility(req.Visibility)
	if err != nil {
		writeInvalidField(w, h.logger, "visibility", `must be "public" or "private"`)
		return
	}

	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	if err := h.bins.Edit(r.Context(), viewer, id, req.Name, req.Description, ownerID, visibility); err != nil {
		writeDomainError(w, r, h.logger, err, "bins api: update")
		return
	}
	view, err := h.bins.GetByID(r.Context(), viewer, id)
	if err != nil {
		writeDomainError(w, r, h.logger, err, "bins api: update: reload")
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, binResponseFrom(view.Bin, view.ItemCount))
}

// Delete handles DELETE /api/v1/bins/{id}: 204 on success, 404 for an
// unknown or invisible bin, 409 when it still holds an item.
func (h *BinsAPIHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePathID(w, r, h.logger, "id", domain.ParseBinID)
	if !ok {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	if err := h.bins.Delete(r.Context(), viewer, id); err != nil {
		writeDomainError(w, r, h.logger, err, "bins api: delete")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// logAndWrite500 logs op's failure and answers a logged 500 — List's own
// two read paths share this rather than each repeating the log-then-write
// pair.
func (h *BinsAPIHandlers) logAndWrite500(w http.ResponseWriter, r *http.Request, op string, err error) {
	h.logger.ErrorContext(r.Context(), op, "error", err)
	api.WriteError(w, h.logger, http.StatusInternalServerError, api.CodeInternal, errInternalServerError)
}

// binViewCursorKey is List's own cursorKeyFunc: code-then-id, the exact
// order BinRepository.ListVisible/ListVisibleByLocation both return
// (ordered by code, unique so nothing ever actually ties).
func binViewCursorKey(v app.BinView) (key, id string) {
	return v.Bin.Code, v.Bin.ID.String()
}

// binResponseFrom projects a domain.Bin plus its own visible item count into
// the response shape — the one place every bin endpoint above builds it.
func binResponseFrom(b domain.Bin, itemCount int) binResponse {
	return binResponse{
		ID:          b.ID.String(),
		Code:        b.Code,
		Name:        b.Name,
		Description: b.Description,
		LocationID:  b.LocationID.String(),
		OwnerID:     optionalIDString(b.OwnerID),
		Visibility:  b.Visibility.String(),
		ItemCount:   itemCount,
		CreatedAt:   b.CreatedAt,
		UpdatedAt:   b.UpdatedAt,
	}
}

// binResponses projects List's own enriched read model into the response's
// per-row shape.
func binResponses(views []app.BinView) []binResponse {
	out := make([]binResponse, 0, len(views))
	for _, v := range views {
		out = append(out, binResponseFrom(v.Bin, v.ItemCount))
	}
	return out
}
