package main

import (
	"log/slog"
	"net/http"

	"github.com/a-h/templ"

	"github.com/ericfisherdev/nestcore/httpserver"
	"github.com/ericfisherdev/nestcore/render"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/web"
	"github.com/ericfisherdev/nestorage/web/components"
)

// shellIconClass sizes every sidebar nav icon uniformly.
const shellIconClass = "h-[21px] w-[21px] flex-none"

// binsPageTitle names the one page this ticket's demo route serves: the
// "All bins" nav entry, page title, and toolbar heading all have to agree,
// so it is named once rather than repeated as three separate literals.
const binsPageTitle = "All bins"

// shellHandlers serves the application shell: the embedded static assets and
// a demo /bins page proving the Hearth shell renders and HTMX fragment swaps
// work. Owners, stats, and the bin toolbar are hard-coded here — Sprint 3
// (identity) and Sprint 4 (bins & items) replace them with real queries;
// this ticket only has to prove the shell around them.
type shellHandlers struct {
	logger *slog.Logger
}

// newShellHandlers constructs shellHandlers. It panics on a nil logger,
// matching every other WebHandlers constructor in this codebase (see
// Nestova's tracking/adapter.NewWebHandlers), so a misconfigured composition
// root is caught at startup rather than at the first request.
func newShellHandlers(logger *slog.Logger) *shellHandlers {
	if logger == nil {
		panic("main: newShellHandlers requires a non-nil logger")
	}
	return &shellHandlers{logger: logger}
}

// Routes registers the shell's routes on mux: the embedded static assets,
// the root redirect, and the demo bins page.
func (h *shellHandlers) Routes(mux *http.ServeMux) {
	mux.Handle("GET /static/", http.StripPrefix("/static/", httpserver.StaticFileServer(web.StaticFS())))
	mux.HandleFunc("GET /{$}", h.handleRoot)
	mux.HandleFunc("GET /bins", h.handleBins)
}

// newAppRoutes composes every route group into the one func value that
// plugs into httpserver.Deps.Routes: the shell's demo pages and static
// assets, plus the identity context's first-run onboarding wizard.
func newAppRoutes(logger *slog.Logger, onboarding *identityadapter.OnboardingHandlers) func(mux *http.ServeMux) {
	shell := newShellHandlers(logger)
	return func(mux *http.ServeMux) {
		shell.Routes(mux)
		onboarding.Routes(mux)
	}
}

// handleRoot sends the app's one entry point, /bins, until there is more
// than one page to land on.
func (h *shellHandlers) handleRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/bins", http.StatusSeeOther)
}

// handleBins renders the shell around a demo toolbar. render.Page returns
// the bare content fragment for an HX-Request, which is what proves the
// filter pills' hx-get/hx-target/hx-swap wiring (web/components/toolbar.templ)
// works end to end without a second endpoint.
func (h *shellHandlers) handleBins(w http.ResponseWriter, r *http.Request) {
	layout := func(content templ.Component) templ.Component {
		return components.Layout(shellProps(), shellNav(), content)
	}
	content := components.Toolbar(binsToolbarView())
	if err := render.Page(r.Context(), w, r, layout, content); err != nil {
		h.logger.ErrorContext(r.Context(), "shell: render bins page", "error", err)
	}
}

// shellNav is the sidebar's primary navigation. The four not-yet-built pages
// link out now so the full nav renders and is reachable per the AC; each
// gets a real handler alongside the feature it belongs to.
func shellNav() []components.NavItem {
	return []components.NavItem{
		{Label: binsPageTitle, Href: "/bins", Active: true, Icon: components.IconBin(shellIconClass)},
		{Label: "Search & find", Href: "/search", Icon: components.IconSearch(shellIconClass)},
		{Label: "Categories", Href: "/categories", Icon: components.IconCategories(shellIconClass)},
		{Label: "Locations", Href: "/locations", Icon: components.IconLocations(shellIconClass)},
		{Label: "Labels & codes", Href: "/labels", Icon: components.IconLabels(shellIconClass)},
	}
}

// shellOwners is the demo Owners list. Sprint 3 (identity) replaces this
// with the household's real members.
func shellOwners() []components.OwnerView {
	return []components.OwnerView{
		{Name: "Maya", Initials: "M", Color: components.OwnerIndigo},
		{Name: "Daniel", Initials: "D", Color: components.OwnerSteel},
		{Name: "Ivy", Initials: "I", Color: components.OwnerTeal},
		{Name: "Leo", Initials: "L", Color: components.OwnerPeri},
		{Name: "Family", Initials: "F", Color: components.OwnerShared},
	}
}

// shellProps is the demo ShellProps. Sprint 4 (bins & items) replaces the
// hard-coded stats with a real query.
func shellProps() components.ShellProps {
	return components.ShellProps{
		Title:  binsPageTitle,
		Owners: shellOwners(),
		Stats:  components.ShellStats{Bins: 8, Items: 201, Rooms: 5},
	}
}

// binsToolbarView is the demo ToolbarView. Sprint 4 replaces the hard-coded
// count and category set with values derived from the household's actual
// bins.
func binsToolbarView() components.ToolbarView {
	return components.ToolbarView{
		Heading:    binsPageTitle,
		Count:      "8 containers",
		Categories: []string{"All", "Seasonal", "Tools", "Keepsakes", "Outdoor", "Toys", "Food"},
		Active:     "All",
	}
}
