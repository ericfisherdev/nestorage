package adapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestcore/render"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
	"github.com/ericfisherdev/nestorage/web/components"
)

// locationQueryCommandService is the narrow port (ISP) LocationsWebHandlers
// depends on, satisfied by *app.LocationService.
type locationQueryCommandService interface {
	List(ctx context.Context, viewer identity.Principal) ([]app.LocationSummary, error)
	Get(ctx context.Context, viewer identity.Principal, id domain.LocationID) (*domain.Location, error)
	Create(ctx context.Context, name, description string, parentID *domain.LocationID, createdBy identity.UserID) (*domain.Location, error)
	Rename(ctx context.Context, id domain.LocationID, name string) error
	Delete(ctx context.Context, id domain.LocationID) error
}

// locationBinLister is the narrow port (ISP) LocationsWebHandlers depends
// on to render a location's own bin list, satisfied by *app.BinService (a
// superset, via ListVisibleByLocation). Named for the single method it
// exposes, per Go's single-method-interface naming convention (io.Reader,
// fmt.Stringer, ...) — mirrors app.binFinder's own naming rationale
// (operations.go).
type locationBinLister interface {
	ListVisibleByLocation(ctx context.Context, viewer identity.Principal, locationID domain.LocationID) ([]app.BinView, error)
}

// LocationsWebHandlers serves the location index/detail/CRUD screens
// (NSTR-31), mirroring identity/adapter/users_web.go's own shape (see
// BinsWebHandlers' identical doc). Every route is registered on its own
// mux, mounted behind RequireAuthenticated by the composition root
// (cmd/server/main.go).
type LocationsWebHandlers struct {
	locations locationQueryCommandService
	bins      locationBinLister
	sm        *scs.SessionManager
	layout    requestLayoutFunc
	logger    *slog.Logger
}

// NewLocationsWebHandlers constructs LocationsWebHandlers. All dependencies
// are required; a missing one panics at construction time, matching every
// other WebHandlers constructor in this codebase.
func NewLocationsWebHandlers(locations locationQueryCommandService, bins locationBinLister, sm *scs.SessionManager, layout requestLayoutFunc, logger *slog.Logger) *LocationsWebHandlers {
	if locations == nil {
		panic("storage/adapter: NewLocationsWebHandlers requires a non-nil locationQueryCommandService")
	}
	if bins == nil {
		panic("storage/adapter: NewLocationsWebHandlers requires a non-nil locationBinLister")
	}
	if sm == nil {
		panic("storage/adapter: NewLocationsWebHandlers requires a non-nil session manager")
	}
	if layout == nil {
		panic("storage/adapter: NewLocationsWebHandlers requires a non-nil layout func")
	}
	if logger == nil {
		panic("storage/adapter: NewLocationsWebHandlers requires a non-nil logger")
	}
	return &LocationsWebHandlers{locations: locations, bins: bins, sm: sm, layout: layout, logger: logger}
}

// Routes registers the location index/detail/CRUD routes on mux.
func (h *LocationsWebHandlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /locations", h.List)
	mux.HandleFunc("POST /locations", h.Create)
	mux.HandleFunc("GET /locations/{id}", h.Detail)
	mux.HandleFunc("GET /locations/{id}/edit", h.EditForm)
	mux.HandleFunc("POST /locations/{id}", h.Update)
	mux.HandleFunc("POST /locations/{id}/delete", h.Delete)
}

// List handles GET /locations.
func (h *LocationsWebHandlers) List(w http.ResponseWriter, r *http.Request) {
	h.renderIndex(w, r, http.StatusOK, "", "")
}

// Create handles POST /locations: CSRF, validate the create form, create
// via LocationService, then finish the way every mutation on this index
// does.
func (h *LocationsWebHandlers) Create(w http.ResponseWriter, r *http.Request) {
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	name := strings.TrimSpace(r.FormValue("name"))
	if _, err := h.locations.Create(r.Context(), name, "", nil, viewer.UserID); err != nil {
		status, msg, ok := mapLocationError(err)
		if !ok {
			h.logger.ErrorContext(r.Context(), "locations: create", "error", err)
			http.Error(w, errInternalServerError, http.StatusInternalServerError)
			return
		}
		h.renderIndex(w, r, status, msg, name)
		return
	}
	h.finishIndexMutation(w, r)
}

// Detail handles GET /locations/{id}: the location's header and its own
// bins.
func (h *LocationsWebHandlers) Detail(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathLocationID(w, r)
	if !ok {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	h.renderDetail(w, r, viewer, id, http.StatusOK, "")
}

// EditForm handles GET /locations/{id}/edit: the rename form pre-filled
// from the current location.
func (h *LocationsWebHandlers) EditForm(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathLocationID(w, r)
	if !ok {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	l, err := h.locations.Get(r.Context(), viewer, id)
	if err != nil {
		h.handleGetError(w, r, err, "locations: edit form")
		return
	}
	view := components.LocationFormView{CSRFToken: session.CSRFToken(r.Context(), h.sm), ID: l.ID.String(), Name: l.Name}
	h.renderFormPage(w, r, http.StatusOK, view)
}

// Update handles POST /locations/{id}: CSRF, validate, rename via
// LocationService, then redirect to the location's detail page.
func (h *LocationsWebHandlers) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathLocationID(w, r)
	if !ok {
		return
	}
	if !verifyRequest(w, r, h.sm) {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if err := h.locations.Rename(r.Context(), id, name); err != nil {
		status, msg, ok := mapLocationError(err)
		if !ok {
			h.logger.ErrorContext(r.Context(), "locations: update", "error", err)
			http.Error(w, errInternalServerError, http.StatusInternalServerError)
			return
		}
		view := components.LocationFormView{CSRFToken: session.CSRFToken(r.Context(), h.sm), ID: id.String(), Name: name, FormError: msg}
		h.renderFormPage(w, r, status, view)
		return
	}
	redirectTo(w, r, "/locations/"+id.String())
}

// Delete handles POST /locations/{id}/delete: on success there is nothing
// left to redraw in place, so this redirects to /locations; on a rejected
// delete (the location still holds a bin or a child location) it
// re-renders the same detail fragment with the error.
func (h *LocationsWebHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathLocationID(w, r)
	if !ok {
		return
	}
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	if err := h.locations.Delete(r.Context(), id); err != nil {
		status, msg, ok := mapLocationError(err)
		if !ok {
			h.logger.ErrorContext(r.Context(), "locations: delete", "error", err)
			http.Error(w, errInternalServerError, http.StatusInternalServerError)
			return
		}
		h.renderDetail(w, r, viewer, id, status, msg)
		return
	}
	redirectTo(w, r, "/locations")
}

// pathLocationID parses the {id} path value, answering 400 and reporting
// ok=false on a malformed one.
func (h *LocationsWebHandlers) pathLocationID(w http.ResponseWriter, r *http.Request) (domain.LocationID, bool) {
	id, err := domain.ParseLocationID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return domain.LocationID{}, false
	}
	return id, true
}

// handleGetError answers a failed location lookup: 404 for
// ErrLocationNotFound, logged 500 otherwise.
func (h *LocationsWebHandlers) handleGetError(w http.ResponseWriter, r *http.Request, err error, op string) {
	if errors.Is(err, domain.ErrLocationNotFound) {
		http.NotFound(w, r)
		return
	}
	h.logger.ErrorContext(r.Context(), op, "error", err)
	http.Error(w, errInternalServerError, http.StatusInternalServerError)
}

// finishIndexMutation completes a successful index mutation (Create): an
// HTMX request gets the re-rendered index fragment, a full navigation gets
// redirected to /locations so a refresh does not resubmit — mirroring
// identity/adapter's UsersWebHandlers.finishMutation exactly.
func (h *LocationsWebHandlers) finishIndexMutation(w http.ResponseWriter, r *http.Request) {
	if render.IsHTMX(r) {
		h.renderIndex(w, r, http.StatusOK, "", "")
		return
	}
	http.Redirect(w, r, "/locations", http.StatusSeeOther)
}

// renderIndex loads the current location list and renders the index at
// status, carrying formError and the create form's pending name.
func (h *LocationsWebHandlers) renderIndex(w http.ResponseWriter, r *http.Request, status int, formError, newName string) {
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	summaries, err := h.locations.List(r.Context(), viewer)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "locations: list", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}

	cards := make([]components.LocationCardView, 0, len(summaries))
	for _, s := range summaries {
		cards = append(cards, components.LocationCardView{ID: s.Location.ID.String(), Name: s.Location.Name, BinCount: s.BinCount})
	}
	view := components.LocationsView{
		CSRFToken: session.CSRFToken(r.Context(), h.sm),
		Locations: cards,
		FormError: formError,
		NewName:   newName,
	}
	content := components.LocationIndex(view)
	if !render.IsHTMX(r) {
		content = h.layout(r, content)
	}
	if err := render.Render(r.Context(), w, status, content); err != nil {
		h.logger.ErrorContext(r.Context(), "locations: render index", "error", err)
	}
}

// renderDetail loads location id's header and its own bins, rendering the
// detail fragment/page at status, carrying formError.
func (h *LocationsWebHandlers) renderDetail(w http.ResponseWriter, r *http.Request, viewer identity.Principal, id domain.LocationID, status int, formError string) {
	l, err := h.locations.Get(r.Context(), viewer, id)
	if err != nil {
		h.handleGetError(w, r, err, "locations: detail")
		return
	}
	binViews, err := h.bins.ListVisibleByLocation(r.Context(), viewer, id)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "locations: detail: list bins", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}

	view := components.LocationDetailView{
		CSRFToken: session.CSRFToken(r.Context(), h.sm),
		ID:        l.ID.String(),
		Name:      l.Name,
		Bins:      buildLocationBinCards(binViews, l.Name, viewer),
		FormError: formError,
	}
	content := components.LocationDetail(view)
	if !render.IsHTMX(r) {
		content = h.layout(r, content)
	}
	if err := render.Render(r.Context(), w, status, content); err != nil {
		h.logger.ErrorContext(r.Context(), "locations: render detail", "error", err)
	}
}

// renderFormPage renders the location edit form at status: the bare
// fragment for an HX-Request, the full shell page for a normal navigation.
func (h *LocationsWebHandlers) renderFormPage(w http.ResponseWriter, r *http.Request, status int, view components.LocationFormView) {
	content := components.LocationForm(view)
	if !render.IsHTMX(r) {
		content = h.layout(r, content)
	}
	if err := render.Render(r.Context(), w, status, content); err != nil {
		h.logger.ErrorContext(r.Context(), "locations: render form", "error", err)
	}
}

// buildLocationBinCards projects a location's own visible bins into the
// card grid's view model (see buildBinCard) — every card shares
// locationName since they are all, definitionally, in the same location.
func buildLocationBinCards(views []app.BinView, locationName string, viewer identity.Principal) []components.BinCardView {
	cards := make([]components.BinCardView, 0, len(views))
	for i, v := range views {
		cards = append(cards, buildBinCard(v, locationName, viewer, i))
	}
	return cards
}

// mapLocationError maps a domain error from a LocationService call to the
// HTTP status and inline message the index/detail/form re-renders with. ok
// reports whether err was recognized; an unrecognized error is the
// caller's cue to log it and answer a generic 500 instead.
func mapLocationError(err error) (status int, message string, ok bool) {
	switch {
	case errors.Is(err, domain.ErrInvalidLocationName):
		return http.StatusUnprocessableEntity, "Please enter a name (up to 100 characters).", true
	case errors.Is(err, domain.ErrLocationNotEmpty):
		return http.StatusConflict, "This location still has bins or child locations — move or remove them first.", true
	case errors.Is(err, domain.ErrLocationNotFound):
		return http.StatusNotFound, "That location no longer exists.", true
	default:
		return 0, "", false
	}
}
