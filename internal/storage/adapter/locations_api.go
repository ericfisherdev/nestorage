package adapter

import (
	"log/slog"
	"net/http"
	"time"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/platform/api"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// LocationsAPIHandlers serves NSTR-54's public JSON CRUD surface for
// locations, delegating to the exact same locationQueryCommandService port
// LocationsWebHandlers already depends on (locations_web.go) — no rule is
// duplicated between the HTML and JSON surfaces, since both call the same
// *app.LocationService methods. Every route is registered on the api.Router
// NSTR-53 mounts behind its bearer-only gate (see cmd/server/shell.go's
// newAPIRouteMount); this type carries no session and no CSRF dependency,
// unlike its web counterpart.
type LocationsAPIHandlers struct {
	locations locationQueryCommandService
	logger    *slog.Logger
}

// NewLocationsAPIHandlers constructs LocationsAPIHandlers. Both dependencies
// are required; a missing one panics at construction time, matching every
// other WebHandlers/APIHandlers constructor in this codebase.
func NewLocationsAPIHandlers(locations locationQueryCommandService, logger *slog.Logger) *LocationsAPIHandlers {
	if locations == nil {
		panic("storage/adapter: NewLocationsAPIHandlers requires a non-nil locationQueryCommandService")
	}
	if logger == nil {
		panic("storage/adapter: NewLocationsAPIHandlers requires a non-nil logger")
	}
	return &LocationsAPIHandlers{locations: locations, logger: logger}
}

// Routes registers the location CRUD routes on mux — an api.Registrar (DIP),
// never the concrete *api.Router, so NSTR-57's OpenAPI spec-sync test can
// substitute a recording double (see api.Registrar's own doc).
func (h *LocationsAPIHandlers) Routes(mux api.Registrar) {
	mux.HandleFunc("GET /api/v1/locations", h.List)
	mux.HandleFunc("POST /api/v1/locations", h.Create)
	mux.HandleFunc("GET /api/v1/locations/{id}", h.Get)
	mux.HandleFunc("PUT /api/v1/locations/{id}", h.Update)
	mux.HandleFunc("DELETE /api/v1/locations/{id}", h.Delete)
}

// locationResponse is every location endpoint's bare per-location shape —
// Get, Create, and Update's own response, and the fields locationListItem
// embeds for List's enriched one.
type locationResponse struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	ParentID    *string   `json:"parent_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// locationListItem is List's own per-row shape: locationResponse plus
// bin_count — the one field only app.LocationService.List's enriched read
// carries (see app.LocationSummary's own doc), so Get/Create/Update
// deliberately do not carry it rather than pay for a second query to fake
// one.
type locationListItem struct {
	locationResponse
	BinCount int `json:"bin_count"`
}

// locationsListResponse is GET /api/v1/locations' response envelope:
// next_cursor is always present, null on the last page (see cursorOrNil's
// own doc).
type locationsListResponse struct {
	Locations  []locationListItem `json:"locations"`
	NextCursor *string            `json:"next_cursor"`
}

// locationCreateRequest is POST /api/v1/locations' request body.
type locationCreateRequest struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	ParentID    *string `json:"parent_id"`
}

// locationRenameRequest is PUT /api/v1/locations/{id}'s request body — name
// is the one mutable field LocationService.Rename exposes.
type locationRenameRequest struct {
	Name string `json:"name"`
}

// List handles GET /api/v1/locations: every location, paginated by name
// (tie-broken by id), each carrying how many bins the caller may see inside
// it.
func (h *LocationsAPIHandlers) List(w http.ResponseWriter, r *http.Request) {
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	summaries, err := h.locations.List(r.Context(), viewer)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "locations api: list", "error", err)
		api.WriteError(w, h.logger, http.StatusInternalServerError, api.CodeInternal, errInternalServerError)
		return
	}

	window, next, err := page(summaries, requestCursor(r), requestLimit(r), locationSummaryCursorKey)
	if err != nil {
		writeMalformedCursor(w, h.logger)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, locationsListResponse{
		Locations:  locationListItems(window),
		NextCursor: cursorOrNil(next),
	})
}

// Create handles POST /api/v1/locations: 403 for an integration principal
// (requireUserPrincipal), otherwise decode and delegate to
// LocationService.Create.
func (h *LocationsAPIHandlers) Create(w http.ResponseWriter, r *http.Request) {
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	if !requireUserPrincipal(w, h.logger, viewer) {
		return
	}

	var req locationCreateRequest
	if !decodeJSONBody(w, r, h.logger, &req) {
		return
	}
	parentID, err := parseOptionalID(req.ParentID, domain.ParseLocationID)
	if err != nil {
		writeMalformedField(w, h.logger, "parent_id")
		return
	}

	l, err := h.locations.Create(r.Context(), req.Name, req.Description, parentID, viewer)
	if err != nil {
		writeDomainError(w, r, h.logger, err, "locations api: create")
		return
	}
	api.WriteJSON(w, h.logger, http.StatusCreated, locationDTO(*l))
}

// Get handles GET /api/v1/locations/{id}.
func (h *LocationsAPIHandlers) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePathID(w, r, h.logger, "id", domain.ParseLocationID)
	if !ok {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	l, err := h.locations.Get(r.Context(), viewer, id)
	if err != nil {
		writeDomainError(w, r, h.logger, err, "locations api: get")
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, locationDTO(*l))
}

// Update handles PUT /api/v1/locations/{id}: name is the one mutable field
// LocationService exposes (LocationRepository's own doc) — the API mirrors
// the service, it does not invent a description/parent_id update.
func (h *LocationsAPIHandlers) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePathID(w, r, h.logger, "id", domain.ParseLocationID)
	if !ok {
		return
	}
	var req locationRenameRequest
	if !decodeJSONBody(w, r, h.logger, &req) {
		return
	}
	if err := h.locations.Rename(r.Context(), id, req.Name); err != nil {
		writeDomainError(w, r, h.logger, err, "locations api: update")
		return
	}

	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	l, err := h.locations.Get(r.Context(), viewer, id)
	if err != nil {
		writeDomainError(w, r, h.logger, err, "locations api: update: reload")
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, locationDTO(*l))
}

// Delete handles DELETE /api/v1/locations/{id}: 204 on success, 409 when the
// location still holds a bin or a child location (domain.ErrLocationNotEmpty).
func (h *LocationsAPIHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePathID(w, r, h.logger, "id", domain.ParseLocationID)
	if !ok {
		return
	}
	if err := h.locations.Delete(r.Context(), id); err != nil {
		writeDomainError(w, r, h.logger, err, "locations api: delete")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// locationSummaryCursorKey is List's own cursorKeyFunc: name-then-id, the
// exact order app.LocationService.List returns (LocationRepository.List's
// own ORDER BY).
func locationSummaryCursorKey(s app.LocationSummary) (key, id string) {
	return s.Location.Name, s.Location.ID.String()
}

// locationDTO projects a domain.Location into its bare response shape.
func locationDTO(l domain.Location) locationResponse {
	return locationResponse{
		ID:          l.ID.String(),
		Name:        l.Name,
		Description: l.Description,
		ParentID:    optionalIDString(l.ParentID),
		CreatedAt:   l.CreatedAt,
		UpdatedAt:   l.UpdatedAt,
	}
}

// locationListItems projects List's own enriched read model into the
// response's per-row shape.
func locationListItems(summaries []app.LocationSummary) []locationListItem {
	items := make([]locationListItem, 0, len(summaries))
	for _, s := range summaries {
		items = append(items, locationListItem{locationResponse: locationDTO(s.Location), BinCount: s.BinCount})
	}
	return items
}
