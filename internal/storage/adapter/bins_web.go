package adapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
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

// binQueryCommandService is the narrow port (ISP) BinsWebHandlers depends
// on, satisfied by *app.BinService.
type binQueryCommandService interface {
	ListVisible(ctx context.Context, viewer identity.Principal) ([]app.BinView, error)
	GetByID(ctx context.Context, viewer identity.Principal, id domain.BinID) (*app.BinView, error)
	GetByCode(ctx context.Context, viewer identity.Principal, code string) (*app.BinView, error)
	Create(ctx context.Context, code, name, description string, locationID domain.LocationID, ownerID *identity.UserID, visibility domain.Visibility, createdBy identity.UserID) (*domain.Bin, error)
	Edit(ctx context.Context, viewer identity.Principal, id domain.BinID, name, description string, ownerID *identity.UserID, visibility domain.Visibility) error
	Delete(ctx context.Context, viewer identity.Principal, id domain.BinID) error
}

// binMover is the narrow port (ISP) BinsWebHandlers depends on to drive the
// move-bin control, satisfied by *app.BinMover. NSTR-31 wires this
// existing service; it does not reimplement moving (see app.BinMover's own
// doc).
type binMover interface {
	Move(ctx context.Context, actor identity.Principal, binID domain.BinID, target domain.LocationID) (app.MoveResult, error)
}

// itemLister is the narrow port (ISP) BinsWebHandlers depends on to render
// a bin's read-only contents list, satisfied by *app.ItemService (a
// superset, via ListInBin).
type itemLister interface {
	ListInBin(ctx context.Context, viewer identity.Principal, binID domain.BinID) ([]domain.Item, error)
}

// BinsWebHandlers serves the bin browse/detail/CRUD/move screens
// (NSTR-31), mirroring identity/adapter/users_web.go's own shape: narrow
// service interfaces (ISP), an injected requestLayoutFunc, the session manager for
// CSRF, a logger; the constructor panics on any nil dependency (DIP +
// fail-fast). Every route is registered on its own mux, mounted behind
// RequireAuthenticated by the composition root (cmd/server/main.go).
type BinsWebHandlers struct {
	bins      binQueryCommandService
	mover     binMover
	locations locationLister
	members   memberDirectory
	items     itemLister
	sm        *scs.SessionManager
	layout    requestLayoutFunc
	logger    *slog.Logger
}

// NewBinsWebHandlers constructs BinsWebHandlers. All dependencies are
// required; a missing one panics at construction time, matching every
// other WebHandlers constructor in this codebase.
func NewBinsWebHandlers(
	bins binQueryCommandService,
	mover binMover,
	locations locationLister,
	members memberDirectory,
	items itemLister,
	sm *scs.SessionManager,
	layout requestLayoutFunc,
	logger *slog.Logger,
) *BinsWebHandlers {
	if bins == nil {
		panic("storage/adapter: NewBinsWebHandlers requires a non-nil binQueryCommandService")
	}
	if mover == nil {
		panic("storage/adapter: NewBinsWebHandlers requires a non-nil binMover")
	}
	if locations == nil {
		panic("storage/adapter: NewBinsWebHandlers requires a non-nil locationLister")
	}
	if members == nil {
		panic("storage/adapter: NewBinsWebHandlers requires a non-nil memberDirectory")
	}
	if items == nil {
		panic("storage/adapter: NewBinsWebHandlers requires a non-nil itemLister")
	}
	if sm == nil {
		panic("storage/adapter: NewBinsWebHandlers requires a non-nil session manager")
	}
	if layout == nil {
		panic("storage/adapter: NewBinsWebHandlers requires a non-nil layout func")
	}
	if logger == nil {
		panic("storage/adapter: NewBinsWebHandlers requires a non-nil logger")
	}
	return &BinsWebHandlers{
		bins: bins, mover: mover, locations: locations, members: members, items: items,
		sm: sm, layout: layout, logger: logger,
	}
}

// Routes registers the bin browse/detail/CRUD/move routes on mux.
func (h *BinsWebHandlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /bins", h.List)
	mux.HandleFunc("GET /bins/new", h.NewForm)
	mux.HandleFunc("POST /bins", h.Create)
	mux.HandleFunc("GET /b/{code}", h.Detail)
	mux.HandleFunc("GET /b/{code}/edit", h.EditForm)
	mux.HandleFunc("POST /b/{code}", h.Update)
	mux.HandleFunc("POST /bins/{id}/delete", h.Delete)
	mux.HandleFunc("POST /bins/{id}/move", h.Move)
}

// List handles GET /bins: every bin viewer may see, as cards.
func (h *BinsWebHandlers) List(w http.ResponseWriter, r *http.Request) {
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	views, err := h.bins.ListVisible(r.Context(), viewer)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "bins: list", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	locations, err := h.locations.List(r.Context(), viewer)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "bins: list locations", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}

	page := components.BinsPageView{
		Toolbar: components.ToolbarView{Heading: "All bins", Count: containerCountLabel(len(views))},
		Bins:    buildBinCards(views, locationNameIndex(locations), viewer),
	}
	// render.Page cannot be used directly here: it takes a request-agnostic
	// layout func, but h.layout is a requestLayoutFunc (real, per-request
	// Owners/Stats/nav need r) — this replicates render.Page's own
	// Vary/IsHTMX shape, mirroring identity/adapter's
	// DeviceTokenWebHandlers.renderPage for the identical reason.
	w.Header().Set("Vary", "HX-Request")
	content := components.BinsPage(page)
	if !render.IsHTMX(r) {
		content = h.layout(r, content)
	}
	if err := render.Render(r.Context(), w, http.StatusOK, content); err != nil {
		h.logger.ErrorContext(r.Context(), "bins: render list", "error", err)
	}
}

// NewForm handles GET /bins/new: the blank create form.
func (h *BinsWebHandlers) NewForm(w http.ResponseWriter, r *http.Request) {
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	view, err := h.buildBinFormView(r.Context(), viewer, "", "", "", "", "", "public", false, "")
	if err != nil {
		h.logger.ErrorContext(r.Context(), "bins: new form", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	h.renderFormPage(w, r, http.StatusOK, view)
}

// Create handles POST /bins: CSRF, validate the create form, create via
// BinService, then redirect to the new bin's detail page.
func (h *BinsWebHandlers) Create(w http.ResponseWriter, r *http.Request) {
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	code := strings.TrimSpace(r.FormValue("code"))
	name := strings.TrimSpace(r.FormValue("name"))
	description := r.FormValue("description")
	locationIDStr := r.FormValue("location_id")
	ownerIDStr := r.FormValue("owner_id")
	visibilityStr := r.FormValue("visibility")

	locationID, ownerID, visibility, msg := parseBinForm(locationIDStr, ownerIDStr, visibilityStr)
	if msg != "" {
		h.renderRejectedForm(w, r, viewer, http.StatusUnprocessableEntity, msg, "", code, name, description, locationIDStr, ownerIDStr, visibilityStr, false)
		return
	}

	b, err := h.bins.Create(r.Context(), code, name, description, locationID, ownerID, visibility, viewer.UserID)
	if err != nil {
		status, mapped, ok := mapBinError(err)
		if !ok {
			h.logger.ErrorContext(r.Context(), "bins: create", "error", err)
			http.Error(w, errInternalServerError, http.StatusInternalServerError)
			return
		}
		h.renderRejectedForm(w, r, viewer, status, mapped, "", code, name, description, locationIDStr, ownerIDStr, visibilityStr, false)
		return
	}
	redirectTo(w, r, "/b/"+b.Code)
}

// Detail handles GET /b/{code}: the bin's header, move control, and
// contents. A non-owner's private bin 404s here exactly as GetByCode's own
// doc promises — it never 403s, so a guessed code cannot even confirm the
// bin exists.
func (h *BinsWebHandlers) Detail(w http.ResponseWriter, r *http.Request) {
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	h.renderDetailByCode(w, r, viewer, r.PathValue("code"), http.StatusOK, "")
}

// EditForm handles GET /b/{code}/edit: the edit form pre-filled from the
// current bin.
func (h *BinsWebHandlers) EditForm(w http.ResponseWriter, r *http.Request) {
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	view, err := h.bins.GetByCode(r.Context(), viewer, r.PathValue("code"))
	if err != nil {
		h.handleGetError(w, r, err, "bins: edit form")
		return
	}
	formView, err := h.buildBinFormView(
		r.Context(), viewer,
		view.Bin.ID.String(), view.Bin.Code, view.Bin.Name, view.Bin.Description,
		ownerIDValue(view.Owner), view.Bin.Visibility.String(), true, "",
	)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "bins: edit form", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	h.renderFormPage(w, r, http.StatusOK, formView)
}

// Update handles POST /b/{code}: CSRF, validate, edit via BinService, then
// redirect back to the bin's detail page.
func (h *BinsWebHandlers) Update(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	existing, err := h.bins.GetByCode(r.Context(), viewer, code)
	if err != nil {
		h.handleGetError(w, r, err, "bins: update")
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	description := r.FormValue("description")
	ownerIDStr := r.FormValue("owner_id")
	visibilityStr := r.FormValue("visibility")

	ownerID, err := parseOwnerID(ownerIDStr)
	if err != nil {
		h.renderRejectedForm(w, r, viewer, http.StatusUnprocessableEntity, "Please choose a valid owner.", existing.Bin.ID.String(), existing.Bin.Code, name, description, "", ownerIDStr, visibilityStr, true)
		return
	}
	visibility, err := domain.ParseVisibility(visibilityStr)
	if err != nil {
		h.renderRejectedForm(w, r, viewer, http.StatusUnprocessableEntity, "Please choose a valid visibility.", existing.Bin.ID.String(), existing.Bin.Code, name, description, "", ownerIDStr, visibilityStr, true)
		return
	}

	if err := h.bins.Edit(r.Context(), viewer, existing.Bin.ID, name, description, ownerID, visibility); err != nil {
		status, mapped, ok := mapBinError(err)
		if !ok {
			h.logger.ErrorContext(r.Context(), "bins: update", "error", err)
			http.Error(w, errInternalServerError, http.StatusInternalServerError)
			return
		}
		h.renderRejectedForm(w, r, viewer, status, mapped, existing.Bin.ID.String(), existing.Bin.Code, name, description, "", ownerIDStr, visibilityStr, true)
		return
	}
	redirectTo(w, r, "/b/"+code)
}

// Delete handles POST /bins/{id}/delete: on success there is nothing left
// to redraw in place, so this redirects to /bins; on a rejected delete
// (the bin still holds an item) it re-renders the same detail fragment
// with the error.
func (h *BinsWebHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathBinID(w, r)
	if !ok {
		return
	}
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	if err := h.bins.Delete(r.Context(), viewer, id); err != nil {
		status, msg, mapped := mapBinError(err)
		if !mapped {
			h.logger.ErrorContext(r.Context(), "bins: delete", "error", err)
			http.Error(w, errInternalServerError, http.StatusInternalServerError)
			return
		}
		h.renderDetailByID(w, r, viewer, id, status, msg)
		return
	}
	redirectTo(w, r, "/bins")
}

// Move handles POST /bins/{id}/move: NSTR-30's app.BinMover.Move, driven by
// movebin.templ's location picker. Both a successful and a rejected move
// re-render the same bin-detail fragment (the bin is not gone either way),
// differing only in status and FormError.
func (h *BinsWebHandlers) Move(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathBinID(w, r)
	if !ok {
		return
	}
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	target, err := domain.ParseLocationID(r.FormValue("location_id"))
	if err != nil {
		h.renderDetailByID(w, r, viewer, id, http.StatusUnprocessableEntity, "Please choose a valid location.")
		return
	}
	if _, err := h.mover.Move(r.Context(), viewer, id, target); err != nil {
		status, msg, mapped := mapBinError(err)
		if !mapped {
			h.logger.ErrorContext(r.Context(), "bins: move", "error", err)
			http.Error(w, errInternalServerError, http.StatusInternalServerError)
			return
		}
		h.renderDetailByID(w, r, viewer, id, status, msg)
		return
	}
	h.renderDetailByID(w, r, viewer, id, http.StatusOK, "")
}

// pathBinID parses the {id} path value, answering 400 and reporting
// ok=false on a malformed one.
func (h *BinsWebHandlers) pathBinID(w http.ResponseWriter, r *http.Request) (domain.BinID, bool) {
	id, err := domain.ParseBinID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return domain.BinID{}, false
	}
	return id, true
}

// handleGetError answers a failed bin lookup: 404 for ErrBinNotFound
// (unknown or invisible — never distinguished, per the visibility
// contract), logged 500 otherwise.
func (h *BinsWebHandlers) handleGetError(w http.ResponseWriter, r *http.Request, err error, op string) {
	if errors.Is(err, domain.ErrBinNotFound) {
		http.NotFound(w, r)
		return
	}
	h.logger.ErrorContext(r.Context(), op, "error", err)
	http.Error(w, errInternalServerError, http.StatusInternalServerError)
}

// renderDetailByCode loads and renders the bin named by code at status,
// carrying formError — the shared tail Detail and a rejected Update's
// error-preserving re-render both need... Update instead redirects on
// success/re-renders the create-form on validation failure, so this is
// currently only called by Detail; kept as its own method (rather than
// inlined into Detail) so a future rejected-Update-in-place design can
// reuse it without duplicating the lookup-and-render shape.
func (h *BinsWebHandlers) renderDetailByCode(w http.ResponseWriter, r *http.Request, viewer identity.Principal, code string, status int, formError string) {
	view, err := h.bins.GetByCode(r.Context(), viewer, code)
	if err != nil {
		h.handleGetError(w, r, err, "bins: detail")
		return
	}
	h.renderDetailView(w, r, viewer, view, status, formError)
}

// renderDetailByID loads and renders the bin named by id at status,
// carrying formError — Delete and Move's shared re-render tail, since both
// routes are id-addressed (see BinsWebHandlers.Routes).
func (h *BinsWebHandlers) renderDetailByID(w http.ResponseWriter, r *http.Request, viewer identity.Principal, id domain.BinID, status int, formError string) {
	view, err := h.bins.GetByID(r.Context(), viewer, id)
	if err != nil {
		h.handleGetError(w, r, err, "bins: detail by id")
		return
	}
	h.renderDetailView(w, r, viewer, view, status, formError)
}

// renderDetailView builds and renders the bin detail fragment/page for an
// already-loaded view.
func (h *BinsWebHandlers) renderDetailView(w http.ResponseWriter, r *http.Request, viewer identity.Principal, view *app.BinView, status int, formError string) {
	items, err := h.items.ListInBin(r.Context(), viewer, view.Bin.ID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "bins: detail: list items", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	locations, err := h.locations.List(r.Context(), viewer)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "bins: detail: list locations", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}

	detail := components.BinDetailView{
		CSRFToken:    session.CSRFToken(r.Context(), h.sm),
		ID:           view.Bin.ID.String(),
		Code:         view.Bin.Code,
		Name:         view.Bin.Name,
		Description:  view.Bin.Description,
		LocationName: locationNameIndex(locations)[view.Bin.LocationID.String()],
		Owner:        ownerView(view.Owner),
		Private:      view.Bin.Visibility.IsPrivate() && view.Bin.CreatedBy == viewer.UserID,
		Items:        buildItemRows(items),
	}
	move := components.MoveBinView{
		CSRFToken:         detail.CSRFToken,
		BinID:             detail.ID,
		BinCode:           detail.Code,
		CurrentLocationID: view.Bin.LocationID.String(),
		Locations:         locationOptions(locations),
		FormError:         formError,
	}
	content := components.BinDetail(detail, move)
	if !render.IsHTMX(r) {
		content = h.layout(r, content)
	}
	if err := render.Render(r.Context(), w, status, content); err != nil {
		h.logger.ErrorContext(r.Context(), "bins: render detail", "error", err)
	}
}

// buildBinFormView loads the location/owner options every bin form needs
// and assembles the view — shared by NewForm, EditForm, and every rejected
// Create/Update re-render.
func (h *BinsWebHandlers) buildBinFormView(
	ctx context.Context, viewer identity.Principal,
	id, code, name, description, ownerID, visibility string, isEdit bool, formError string,
) (components.BinFormView, error) {
	locations, err := h.locations.List(ctx, viewer)
	if err != nil {
		return components.BinFormView{}, err
	}
	members, err := h.members.List(ctx)
	if err != nil {
		return components.BinFormView{}, err
	}
	return components.BinFormView{
		CSRFToken: session.CSRFToken(ctx, h.sm),
		ID:        id, Code: code, Name: name, Description: description,
		Locations: locationOptions(locations), OwnerID: ownerID, Owners: ownerOptions(members),
		Visibility: visibility, IsEdit: isEdit, FormError: formError,
	}, nil
}

// renderRejectedForm re-renders the bin form (create or edit) with
// formError and every pending value preserved — the create/update
// validation-failure tail.
func (h *BinsWebHandlers) renderRejectedForm(
	w http.ResponseWriter, r *http.Request, viewer identity.Principal,
	status int, formError, id, code, name, description, locationID, ownerID, visibility string, isEdit bool,
) {
	view, err := h.buildBinFormView(r.Context(), viewer, id, code, name, description, ownerID, visibility, isEdit, formError)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "bins: rejected form", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	view.LocationID = locationID
	h.renderFormPage(w, r, status, view)
}

// renderFormPage renders the bin form at status: the bare fragment for an
// HX-Request, the full shell page for a normal navigation.
func (h *BinsWebHandlers) renderFormPage(w http.ResponseWriter, r *http.Request, status int, view components.BinFormView) {
	content := components.BinForm(view)
	if !render.IsHTMX(r) {
		content = h.layout(r, content)
	}
	if err := render.Render(r.Context(), w, status, content); err != nil {
		h.logger.ErrorContext(r.Context(), "bins: render form", "error", err)
	}
}

// containerCountLabel formats the toolbar's item count the way the
// reference does ("8 containers", "1 container").
func containerCountLabel(n int) string {
	if n == 1 {
		return "1 container"
	}
	return strconv.Itoa(n) + " containers"
}

// buildBinCards projects BinService's read model into the card grid's view
// model (see buildBinCard), resolving each bin's location name from
// locNames (built once per request, see locationNameIndex) rather than a
// query per bin.
func buildBinCards(views []app.BinView, locNames map[string]string, viewer identity.Principal) []components.BinCardView {
	cards := make([]components.BinCardView, 0, len(views))
	for i, v := range views {
		cards = append(cards, buildBinCard(v, locNames[v.Bin.LocationID.String()], viewer, i))
	}
	return cards
}

// buildItemRows projects a bin's items into the read-only contents list's
// view model.
func buildItemRows(items []domain.Item) []components.ItemRowView {
	rows := make([]components.ItemRowView, 0, len(items))
	for _, it := range items {
		description := ""
		if it.Description != nil {
			description = *it.Description
		}
		rows = append(rows, components.ItemRowView{Name: it.Name, Description: description, Quantity: it.Quantity})
	}
	return rows
}

// parseBinForm validates the create form's location/owner/visibility
// fields together, returning the first human-readable message naming a
// failure (matching identity/adapter's parseNewUserForm's own shape).
func parseBinForm(locationIDStr, ownerIDStr, visibilityStr string) (locationID domain.LocationID, ownerID *identity.UserID, visibility domain.Visibility, message string) {
	locationID, err := domain.ParseLocationID(locationIDStr)
	if err != nil {
		return domain.LocationID{}, nil, "", "Please choose a location."
	}
	ownerID, err = parseOwnerID(ownerIDStr)
	if err != nil {
		return domain.LocationID{}, nil, "", "Please choose a valid owner."
	}
	visibility, err = domain.ParseVisibility(visibilityStr)
	if err != nil {
		return domain.LocationID{}, nil, "", "Please choose a valid visibility."
	}
	return locationID, ownerID, visibility, ""
}

// parseOwnerID parses the owner <select>'s value: empty means the shared/
// Family bin (nil), matching ownerOptions' own contract.
func parseOwnerID(s string) (*identity.UserID, error) {
	if s == "" {
		return nil, nil
	}
	id, err := identity.ParseUserID(s)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// mapBinError maps a domain error from a BinService/app.BinMover call to
// the HTTP status and inline message the form/detail fragment re-renders
// with. ok reports whether err was recognized; an unrecognized error is the
// caller's cue to log it and answer a generic 500 instead.
func mapBinError(err error) (status int, message string, ok bool) {
	switch {
	case errors.Is(err, domain.ErrInvalidBin):
		return http.StatusUnprocessableEntity, "Please check the bin's name and code.", true
	case errors.Is(err, domain.ErrDuplicateBinCode):
		return http.StatusUnprocessableEntity, "That code is already in use.", true
	case errors.Is(err, domain.ErrLocationNotFound):
		return http.StatusUnprocessableEntity, "Please choose a valid location.", true
	case errors.Is(err, identity.ErrUserNotFound):
		return http.StatusUnprocessableEntity, "Please choose a valid owner.", true
	case errors.Is(err, domain.ErrBinNotEmpty):
		return http.StatusConflict, "This bin still has items in it — remove them first.", true
	case errors.Is(err, domain.ErrBinAlreadyInLocation):
		return http.StatusUnprocessableEntity, "That bin is already in that location.", true
	case errors.Is(err, domain.ErrBinNotFound):
		return http.StatusNotFound, "That bin no longer exists.", true
	default:
		return 0, "", false
	}
}
