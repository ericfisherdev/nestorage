package adapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestcore/render"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
	"github.com/ericfisherdev/nestorage/web/components"
)

// msgInvalidBin is the return control's validation message, named once
// (mirroring bins_web.go's own msgInvalid* constants) so parseBinForm's
// analog here and mapItemOperationError never drift apart on wording
// (SonarCloud flagged this class of duplication, go:S1192).
const msgInvalidBin = "Please choose a valid bin."

// itemQueryService is the narrow port (ISP) ItemsWebHandlers depends on for
// item detail and search, satisfied by *app.ItemQueryService.
type itemQueryService interface {
	Detail(ctx context.Context, viewer identity.Principal, id domain.ItemID) (*domain.ItemDetailResult, error)
	Search(ctx context.Context, viewer identity.Principal, rawQuery string) ([]domain.ItemSearchResult, error)
}

// itemOperator is the narrow port (ISP) ItemsWebHandlers depends on to drive
// the detail page's check-out/return controls, satisfied by
// *app.OperationService (a superset, via RemoveFromBin/ReturnToBin). This
// wires NSTR-29's existing service; it does not reimplement checking an item
// in or out.
type itemOperator interface {
	RemoveFromBin(ctx context.Context, actor identity.Principal, itemID domain.ItemID) (app.Operation, error)
	ReturnToBin(ctx context.Context, actor identity.Principal, itemID domain.ItemID, binID domain.BinID) (app.Operation, error)
}

// itemBinLister is the narrow port (ISP) ItemsWebHandlers depends on to
// populate the return control's bin picker, satisfied by *app.BinService (a
// superset, via ListVisible) — the same concrete service BinsWebHandlers
// depends on through its own, differently-shaped binQueryCommandService
// port (ISP: each handler group only ever depends on the methods it uses).
type itemBinLister interface {
	ListVisible(ctx context.Context, viewer identity.Principal) ([]app.BinView, error)
}

// ItemsWebHandlers serves the item detail and search screens (NSTR-32),
// mirroring BinsWebHandlers' own shape: narrow service interfaces (ISP), an
// injected requestLayoutFunc, the session manager for CSRF on the two
// mutating operation controls, a logger; the constructor panics on any nil
// dependency (DIP + fail-fast). Every route is registered on its own mux,
// mounted behind RequireAuthenticated by the composition root
// (cmd/server/main.go).
type ItemsWebHandlers struct {
	items      itemQueryService
	operations itemOperator
	bins       itemBinLister
	sm         *scs.SessionManager
	layout     requestLayoutFunc
	logger     *slog.Logger
}

// ItemsWebHandlersDeps groups NewItemsWebHandlers' dependencies into one
// value instead of a growing parameter list — mirrors BinsWebHandlersDeps'
// own rationale (SonarCloud's go:S107). Every field is still injected
// explicitly by the composition root (cmd/server/main.go).
type ItemsWebHandlersDeps struct {
	Items      itemQueryService
	Operations itemOperator
	Bins       itemBinLister
	SM         *scs.SessionManager
	Layout     requestLayoutFunc
	Logger     *slog.Logger
}

// NewItemsWebHandlers constructs ItemsWebHandlers. All dependencies are
// required; a missing one panics at construction time, matching every other
// WebHandlers constructor in this codebase.
func NewItemsWebHandlers(deps ItemsWebHandlersDeps) *ItemsWebHandlers {
	if deps.Items == nil {
		panic("storage/adapter: NewItemsWebHandlers requires a non-nil itemQueryService")
	}
	if deps.Operations == nil {
		panic("storage/adapter: NewItemsWebHandlers requires a non-nil itemOperator")
	}
	if deps.Bins == nil {
		panic("storage/adapter: NewItemsWebHandlers requires a non-nil itemBinLister")
	}
	if deps.SM == nil {
		panic("storage/adapter: NewItemsWebHandlers requires a non-nil session manager")
	}
	if deps.Layout == nil {
		panic("storage/adapter: NewItemsWebHandlers requires a non-nil layout func")
	}
	if deps.Logger == nil {
		panic("storage/adapter: NewItemsWebHandlers requires a non-nil logger")
	}
	return &ItemsWebHandlers{
		items: deps.Items, operations: deps.Operations, bins: deps.Bins,
		sm: deps.SM, layout: deps.Layout, logger: deps.Logger,
	}
}

// Routes registers the item detail/search/operation routes on mux.
func (h *ItemsWebHandlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /search", h.Search)
	mux.HandleFunc("GET /items/{id}", h.Detail)
	mux.HandleFunc("POST /items/{id}/check-out", h.CheckOut)
	mux.HandleFunc("POST /items/{id}/return", h.Return)
}

// Search handles GET /search: the full shell page (heading, type-ahead box,
// results) on a normal navigation, or the bare results fragment on the
// type-ahead box's own HTMX request — the one endpoint item_search.templ's
// itemSearchAttrs targets for every keystroke, so no second route is
// needed.
func (h *ItemsWebHandlers) Search(w http.ResponseWriter, r *http.Request) {
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	query := r.URL.Query().Get("q")

	results, err := h.items.Search(r.Context(), viewer, query)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "items: search", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}

	view := components.SearchPageView{Query: query, Results: buildSearchResultViews(results)}
	w.Header().Set("Vary", "HX-Request")
	content := components.SearchResults(view)
	if !render.IsHTMX(r) {
		content = h.layout(r, components.ItemSearchPage(view))
	}
	if err := render.Render(r.Context(), w, http.StatusOK, content); err != nil {
		h.logger.ErrorContext(r.Context(), "items: render search", "error", err)
	}
}

// Detail handles GET /items/{id}. An item not visible to viewer 404s here
// exactly as domain.ErrItemNotFound's own doc promises — it never
// distinguishes "unknown" from "not visible", so a guessed id cannot even
// confirm the item exists.
func (h *ItemsWebHandlers) Detail(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	h.renderDetail(w, r, viewer, id, http.StatusOK, "")
}

// CheckOut handles POST /items/{id}/check-out: CSRF, NSTR-29's
// OperationService.RemoveFromBin, then re-render the detail fragment either
// way — a successful check-out has the same fragment shape to redraw
// (holder panel instead of bin panel), and a rejected one (e.g. an
// integration principal, or a lost race) re-renders with FormError set.
func (h *ItemsWebHandlers) CheckOut(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	if _, err := h.operations.RemoveFromBin(r.Context(), viewer, id); err != nil {
		status, msg, mapped := mapItemOperationError(err)
		if !mapped {
			h.logger.ErrorContext(r.Context(), "items: check out", "error", err)
			http.Error(w, errInternalServerError, http.StatusInternalServerError)
			return
		}
		h.renderDetail(w, r, viewer, id, status, msg)
		return
	}
	h.renderDetail(w, r, viewer, id, http.StatusOK, "")
}

// Return handles POST /items/{id}/return: CSRF, parse the target bin_id from
// the return control's picker, NSTR-29's OperationService.ReturnToBin, then
// re-render the detail fragment either way — see CheckOut's own doc for why.
func (h *ItemsWebHandlers) Return(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	binID, err := domain.ParseBinID(r.FormValue("bin_id"))
	if err != nil {
		h.renderDetail(w, r, viewer, id, http.StatusUnprocessableEntity, msgInvalidBin)
		return
	}
	if _, err := h.operations.ReturnToBin(r.Context(), viewer, id, binID); err != nil {
		status, msg, mapped := mapItemOperationError(err)
		if !mapped {
			h.logger.ErrorContext(r.Context(), "items: return", "error", err)
			http.Error(w, errInternalServerError, http.StatusInternalServerError)
			return
		}
		h.renderDetail(w, r, viewer, id, status, msg)
		return
	}
	h.renderDetail(w, r, viewer, id, http.StatusOK, "")
}

// pathItemID parses the {id} path value, answering 400 and reporting
// ok=false on a malformed one.
func (h *ItemsWebHandlers) pathItemID(w http.ResponseWriter, r *http.Request) (domain.ItemID, bool) {
	id, err := domain.ParseItemID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return domain.ItemID{}, false
	}
	return id, true
}

// renderDetail loads and renders item id's detail fragment/page at status,
// carrying formError — Detail's own tail, and CheckOut/Return's shared
// re-render after a successful or rejected operation.
func (h *ItemsWebHandlers) renderDetail(w http.ResponseWriter, r *http.Request, viewer identity.Principal, id domain.ItemID, status int, formError string) {
	result, err := h.items.Detail(r.Context(), viewer, id)
	if err != nil {
		h.handleItemGetError(w, r, err, "items: detail")
		return
	}

	view := buildItemDetailView(*result, session.CSRFToken(r.Context(), h.sm), formError)
	if !view.InBin {
		bins, err := h.bins.ListVisible(r.Context(), viewer)
		if err != nil {
			h.logger.ErrorContext(r.Context(), "items: detail: list bins", "error", err)
			http.Error(w, errInternalServerError, http.StatusInternalServerError)
			return
		}
		view.ReturnBins = buildBinOptions(bins)
	}

	content := components.ItemDetail(view)
	if !render.IsHTMX(r) {
		content = h.layout(r, content)
	}
	if err := render.Render(r.Context(), w, status, content); err != nil {
		h.logger.ErrorContext(r.Context(), "items: render detail", "error", err)
	}
}

// handleItemGetError answers a failed item lookup: 404 for
// domain.ErrItemNotFound (unknown or invisible — never distinguished, per
// the visibility contract), logged 500 otherwise. Named distinctly from
// BinsWebHandlers.handleGetError/LocationsWebHandlers.handleGetError (all
// three coexist in this package) despite the identical shape.
func (h *ItemsWebHandlers) handleItemGetError(w http.ResponseWriter, r *http.Request, err error, op string) {
	if errors.Is(err, domain.ErrItemNotFound) {
		http.NotFound(w, r)
		return
	}
	h.logger.ErrorContext(r.Context(), op, "error", err)
	http.Error(w, errInternalServerError, http.StatusInternalServerError)
}

// mapItemOperationError maps a domain error from an OperationService call to
// the HTTP status and inline message the detail fragment re-renders with.
// ok reports whether err was recognized; an unrecognized error is the
// caller's cue to log it and answer a generic 500 instead — mirrors
// mapBinError/mapLocationError's own shape.
func mapItemOperationError(err error) (status int, message string, ok bool) {
	switch {
	case errors.Is(err, domain.ErrHolderRequired):
		return http.StatusForbidden, "Only a signed-in person can check out an item.", true
	case errors.Is(err, domain.ErrItemAlreadyCheckedOut):
		return http.StatusConflict, "This item is already checked out.", true
	case errors.Is(err, domain.ErrItemNotCheckedOut):
		return http.StatusConflict, "This item is not checked out.", true
	case errors.Is(err, domain.ErrBinNotFound):
		return http.StatusUnprocessableEntity, msgInvalidBin, true
	case errors.Is(err, domain.ErrItemNotFound):
		return http.StatusNotFound, "That item no longer exists.", true
	default:
		return 0, "", false
	}
}

// buildSearchResultViews projects SearchVisible's read model into the
// results list's view model.
func buildSearchResultViews(results []domain.ItemSearchResult) []components.SearchResultView {
	views := make([]components.SearchResultView, 0, len(results))
	for _, r := range results {
		views = append(views, components.SearchResultView{
			ID: r.ID.String(), Name: r.Name, Quantity: r.Quantity, CheckedOut: r.State == domain.StateCheckedOut,
			BinCode: r.BinCode, LocationName: r.LocationName, HolderName: r.HolderName,
		})
	}
	return views
}

// buildBinOptions builds the return control's bin <select> options from
// BinService's own visible-bins read model, mirroring locationOptions' own
// shape.
func buildBinOptions(views []app.BinView) []components.BinOptionView {
	opts := make([]components.BinOptionView, 0, len(views))
	for _, v := range views {
		opts = append(opts, components.BinOptionView{ID: v.Bin.ID.String(), Label: v.Bin.Code + " — " + v.Bin.Name})
	}
	return opts
}

// buildItemDetailView projects an *domain.ItemDetailResult into
// ItemDetail's view model. ReturnBins is left for renderDetail's own caller
// to populate (only when !InBin) since it needs a second, viewer-scoped
// query this projection has no access to.
func buildItemDetailView(result domain.ItemDetailResult, csrfToken, formError string) components.ItemDetailView {
	description := ""
	if result.Item.Description != nil {
		description = *result.Item.Description
	}
	inBin := result.Item.State() == domain.StateInBin
	return components.ItemDetailView{
		CSRFToken: csrfToken, ID: result.Item.ID.String(), Name: result.Item.Name,
		Description: description, Quantity: result.Item.Quantity, InBin: inBin,
		BinCode: result.BinCode, LocationName: result.LocationName,
		Holder:    components.OwnerView{Name: result.HolderName, Initials: holderInitials(result.HolderName), Color: components.ParseOwnerColor(result.HolderColor.String())},
		HeldSince: formatHeldSince(result.Item.PlacementChangedAt),
		FormError: formError,
	}
}

// holderInitials returns the first letter of name, uppercased, matching
// every other owner-avatar initials helper in this codebase (app/owner.go's
// initials, cmd/server/shell.go's shellInitials) so a checked-out item's
// holder avatar agrees with them. A rune slice is used so a multi-byte
// first character is not split.
func holderInitials(name string) string {
	r := []rune(strings.TrimSpace(name))
	if len(r) == 0 {
		return "?"
	}
	return strings.ToUpper(string(r[0]))
}

// formatHeldSince renders an item's PlacementChangedAt for the AC's "since
// when" sentence — more precise than formatStoredDate's "Oct 2025" (a bin
// card's coarse "stored since" caption), since holding-since is the specific
// fact the detail page's headline states.
func formatHeldSince(t time.Time) string {
	return t.Format("Jan 2, 2006")
}
