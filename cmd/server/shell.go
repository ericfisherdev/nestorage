package main

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/a-h/templ"

	"github.com/ericfisherdev/nestcore/httpserver"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	storageadapter "github.com/ericfisherdev/nestorage/internal/storage/adapter"
	storageapp "github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/web"
	"github.com/ericfisherdev/nestorage/web/components"
)

// shellIconClass sizes every sidebar nav icon uniformly.
const shellIconClass = "h-[21px] w-[21px] flex-none"

// binsPageTitle names the "All bins" nav entry, page title, and toolbar
// heading, which all have to agree, so it is named once rather than
// repeated as three separate literals.
const binsPageTitle = "All bins"

// locationsPageTitle names NSTR-31's location index/detail pages.
const locationsPageTitle = "Locations"

// searchPageTitle names NSTR-32's item search page and detail page — see
// newStorageLayout's own doc for why a single fixed title covers every
// route a handler group serves, the same convention binsPageTitle already
// follows for both /bins and a specific bin's own /b/{code} detail page.
const searchPageTitle = "Search & find"

// usersPageTitle names NSTR-21's admin user-management page.
const usersPageTitle = "Users"

// devicesPageTitle names NSTR-22's device self-service page.
const devicesPageTitle = "Devices"

// apiKeySettingsPageTitle names NSTR-23's account api key management page.
const apiKeySettingsPageTitle = "API key"

// shellHandlers serves the application shell: the embedded static assets
// and the root redirect. NSTR-31 removed this type's own demo /bins route
// (handleBins) and its hard-coded Owners/Stats — BinsWebHandlers now owns
// /bins for real, and shellDataService (below) computes real Owners/Stats
// for every page's layout closure.
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

// Routes registers the shell's routes on mux: the embedded static assets
// and the root redirect.
func (h *shellHandlers) Routes(mux *http.ServeMux) {
	mux.Handle("GET /static/", http.StripPrefix("/static/", httpserver.StaticFileServer(web.StaticFS())))
	mux.HandleFunc("GET /{$}", h.handleRoot)
}

// shellMemberLister is the narrow port (ISP) shellDataService depends on
// for the sidebar's real Owners list, satisfied by identity's
// UserRepository (a superset, via List).
type shellMemberLister interface {
	List(ctx context.Context) ([]identity.User, error)
}

// shellBinLister is the narrow port (ISP) shellDataService depends on for
// the sidebar's real bin/item counts, satisfied by *storageapp.BinService
// (a superset, via ListVisible).
type shellBinLister interface {
	ListVisible(ctx context.Context, viewer identity.Principal) ([]storageapp.BinView, error)
}

// shellLocationLister is the narrow port (ISP) shellDataService depends on
// for the sidebar's real room count, satisfied by
// *storageapp.LocationService (a superset, via List).
type shellLocationLister interface {
	List(ctx context.Context, viewer identity.Principal) ([]storageapp.LocationSummary, error)
}

// shellDataService computes ShellProps' real Owners/Stats per request,
// replacing the removed shellOwners()/hard-coded ShellStats demo data.
// Stats is scoped by viewer through the same ListVisible/List calls the
// bin grid and location index themselves use, so the sidebar summary can
// never hint at a private bin a non-owner cannot otherwise see.
type shellDataService struct {
	members   shellMemberLister
	bins      shellBinLister
	locations shellLocationLister
}

// newShellDataService constructs shellDataService. All dependencies are
// required; a missing one panics at construction time, matching every other
// constructor in this codebase.
func newShellDataService(members shellMemberLister, bins shellBinLister, locations shellLocationLister) *shellDataService {
	if members == nil {
		panic("main: newShellDataService requires a non-nil shellMemberLister")
	}
	if bins == nil {
		panic("main: newShellDataService requires a non-nil shellBinLister")
	}
	if locations == nil {
		panic("main: newShellDataService requires a non-nil shellLocationLister")
	}
	return &shellDataService{members: members, bins: bins, locations: locations}
}

// Owners returns the sidebar's real Owners list: one entry per household
// member plus the shared/Family entry every bin without an owner wears.
func (s *shellDataService) Owners(ctx context.Context) ([]components.OwnerView, error) {
	members, err := s.members.List(ctx)
	if err != nil {
		return nil, err
	}
	owners := make([]components.OwnerView, 0, len(members)+1)
	for _, m := range members {
		owners = append(owners, components.OwnerView{
			Name:     m.DisplayName,
			Initials: shellInitials(m.DisplayName),
			Color:    components.ParseOwnerColor(m.Color.String()),
		})
	}
	owners = append(owners, components.OwnerView{Name: "Family", Initials: "F", Color: components.OwnerShared})
	return owners, nil
}

// Stats returns the sidebar's real "Storage at a glance" counts, scoped to
// what viewer may see. Items sums each visible bin's own ItemCount rather
// than querying every item directly — a checked-out (held) item is
// deliberately left out of this sidebar summary, a reasonable
// simplification for a glance figure that undercounts rather than leaks.
func (s *shellDataService) Stats(ctx context.Context, viewer identity.Principal) (components.ShellStats, error) {
	bins, err := s.bins.ListVisible(ctx, viewer)
	if err != nil {
		return components.ShellStats{}, err
	}
	locations, err := s.locations.List(ctx, viewer)
	if err != nil {
		return components.ShellStats{}, err
	}
	items := 0
	for _, b := range bins {
		items += b.ItemCount
	}
	return components.ShellStats{Bins: len(bins), Items: items, Rooms: len(locations)}, nil
}

// shellInitials returns the first letter of name, uppercased, matching
// identity/adapter's own initials helper (users_web.go) so an owner's
// initial always agrees with the admin user-management screen's. A rune
// slice is used so a multi-byte first character is not split.
func shellInitials(name string) string {
	r := []rune(strings.TrimSpace(name))
	if len(r) == 0 {
		return "?"
	}
	return strings.ToUpper(string(r[0]))
}

// newAppRoutes composes every route group into the one func value that
// plugs into httpserver.Deps.Routes: the shell's static assets and root
// redirect, the identity context's first-run onboarding wizard, its
// login/logout routes, NSTR-21's admin user-management routes, NSTR-22's
// device-token exchange (public) and self-service (any signed-in user)
// routes, and NSTR-31's bin/location browse-and-manage routes.
//
// The admin routes are registered on their own mux, mounted at "/admin/"
// behind RequireAdmin alone — NSTR-24's Principal-based RequireAdmin already
// answers 401 for an anonymous request (see its own doc), so it no longer
// needs RequireUser chained in front of it the way the session-based version
// did.
//
// The device self-service routes are registered on their own mux at
// "/settings/", behind RequireUser only — unlike adminMux, no RequireAdmin:
// any signed-in user manages their own devices. This gate is deliberately
// left on the session-based RequireUser (not re-homed onto Principal) per
// NSTR-24's own reconciliation, which only re-homes RequireAdmin. The
// exchange endpoint (deviceTokenAPI) carries no session at all and is
// mounted at the top level alongside login, matching its own doc.
//
// NSTR-23's account api key routes sit under the same "/settings/" path
// prefix as the device screen but, unlike it, need RequireAdmin: the
// credential is account-wide, not per-user. They are mounted on their own
// mux at both "/settings/api-key" (exact) and "/settings/api-key/"
// (subtree), the two registrations together covering the bare path
// (create/view) and its /rotate and /revoke children without a redirect —
// net/http.ServeMux picks the more specific match over the broader
// "/settings/" registration either way.
//
// NSTR-31's bin/location routes (/bins, /b/{code}, /locations, ...), plus
// NSTR-32's item detail/search routes (/search, /items/{id}, ...), share no
// common path prefix to mount a submux under, so they are registered on
// their own mux mounted at the bare "/" catch-all instead, behind
// RequireAuthenticated — every already-registered exact/prefix pattern on
// the outer mux (the root redirect, static assets, login, admin, settings)
// still wins over that catch-all, so this only ever gates a request no more
// specific pattern claimed.
//
// appRouteDeps groups newAppRoutes' dependencies into one value instead of a
// growing parameter list: NSTR-24 added Denier as the eighth, past
// golangci-lint's function-length threshold. Each field is still injected
// explicitly by the composition root (main.go) — this is a grouping of
// constructor arguments, not a service locator.
type appRouteDeps struct {
	Logger         *slog.Logger
	Onboarding     *identityadapter.OnboardingHandlers
	Login          *identityadapter.Handlers
	Users          *identityadapter.UsersWebHandlers
	DeviceTokenAPI *identityadapter.DeviceTokenAPIHandlers
	DeviceTokenWeb *identityadapter.DeviceTokenWebHandlers
	APIKeyWeb      *identityadapter.APIKeyWebHandlers
	Bins           *storageadapter.BinsWebHandlers
	Locations      *storageadapter.LocationsWebHandlers
	Items          *storageadapter.ItemsWebHandlers
	Denier         *identityadapter.Denier
}

func newAppRoutes(deps appRouteDeps) func(mux *http.ServeMux) {
	shell := newShellHandlers(deps.Logger)
	adminGate := identityadapter.RequireAdmin(deps.Denier)
	userGate := identityadapter.RequireUser()
	authGate := identityadapter.RequireAuthenticated(deps.Denier)
	return func(mux *http.ServeMux) {
		shell.Routes(mux)
		deps.Onboarding.Routes(mux)
		deps.Login.Routes(mux)
		deps.DeviceTokenAPI.Routes(mux)

		adminMux := http.NewServeMux()
		deps.Users.Routes(adminMux)
		mux.Handle("/admin/", adminGate(adminMux))

		settingsMux := http.NewServeMux()
		deps.DeviceTokenWeb.Routes(settingsMux)
		mux.Handle("/settings/", userGate(settingsMux))

		apiKeyMux := http.NewServeMux()
		deps.APIKeyWeb.Routes(apiKeyMux)
		gatedAPIKey := adminGate(apiKeyMux)
		mux.Handle("/settings/api-key", gatedAPIKey)
		mux.Handle("/settings/api-key/", gatedAPIKey)

		storageMux := http.NewServeMux()
		deps.Bins.Routes(storageMux)
		deps.Locations.Routes(storageMux)
		deps.Items.Routes(storageMux)
		mux.Handle("/", authGate(storageMux))
	}
}

// handleRoot sends the app's one entry point, /bins, until there is more
// than one page to land on.
func (h *shellHandlers) handleRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/bins", http.StatusSeeOther)
}

// isCurrentUserAdmin reports whether Authenticate resolved an admin user
// into r's context — an anonymous or non-admin request reports false, which
// shellNav treats identically (no Users entry).
func isCurrentUserAdmin(r *http.Request) bool {
	u, ok := identityadapter.CurrentUser(r.Context())
	return ok && u.IsAdmin()
}

// shellProps assembles ShellProps for title from data's real Owners/Stats,
// scoped by ctx's resolved Principal (anonymous if none resolved).
func shellProps(ctx context.Context, data *shellDataService, title string) (components.ShellProps, error) {
	viewer, _ := identityadapter.CurrentPrincipal(ctx)
	owners, err := data.Owners(ctx)
	if err != nil {
		return components.ShellProps{Title: title}, err
	}
	stats, err := data.Stats(ctx, viewer)
	if err != nil {
		return components.ShellProps{Title: title, Owners: owners}, err
	}
	return components.ShellProps{Title: title, Owners: owners, Stats: stats}, nil
}

// newShellLayout returns the request-aware layout func a page whose nav's
// Users entry (and thus shellNav's isAdmin) is fixed for every request
// reaching it shares — NSTR-21's admin screen and NSTR-23's api key
// settings, both already mounted behind RequireAdmin (see newAppRoutes), so
// isAdmin is unconditionally true rather than read per request.
func newShellLayout(data *shellDataService, title string, isAdmin bool, logger *slog.Logger) func(r *http.Request, content templ.Component) templ.Component {
	return func(r *http.Request, content templ.Component) templ.Component {
		props, err := shellProps(r.Context(), data, title)
		if err != nil {
			// Owners/Stats failed to load — log and fall back to whatever
			// shellProps could still assemble rather than failing the whole
			// page: the content the user actually asked for still renders.
			logger.ErrorContext(r.Context(), "shell: load props", "error", err)
		}
		return components.Layout(props, shellNav(r.URL.Path, isAdmin), content)
	}
}

// newAdminUsersLayout returns the layout func injected into
// identityadapter.NewUsersWebHandlers (see newShellLayout's own doc for why
// isAdmin is fixed true here).
func newAdminUsersLayout(data *shellDataService, logger *slog.Logger) func(r *http.Request, content templ.Component) templ.Component {
	return newShellLayout(data, usersPageTitle, true, logger)
}

// newAPIKeySettingsLayout returns the layout func injected into
// identityadapter.NewAPIKeyWebHandlers (see newShellLayout's own doc).
func newAPIKeySettingsLayout(data *shellDataService, logger *slog.Logger) func(r *http.Request, content templ.Component) templ.Component {
	return newShellLayout(data, apiKeySettingsPageTitle, true, logger)
}

// newRequestAdminAwareLayout returns the layout func shared by pages
// reachable by any signed-in user (not only an admin), where shellNav's
// Users entry must reflect the ACTUAL request's signed-in user —
// NSTR-22's device self-service screen and NSTR-31's bin/location pages
// alike.
func newRequestAdminAwareLayout(data *shellDataService, title string, logger *slog.Logger) func(r *http.Request, content templ.Component) templ.Component {
	return func(r *http.Request, content templ.Component) templ.Component {
		props, err := shellProps(r.Context(), data, title)
		if err != nil {
			logger.ErrorContext(r.Context(), "shell: load props", "error", err)
		}
		return components.Layout(props, shellNav(r.URL.Path, isCurrentUserAdmin(r)), content)
	}
}

// newDeviceSettingsLayout returns the layout func injected into
// identityadapter.NewDeviceTokenWebHandlers (see
// newRequestAdminAwareLayout's own doc).
func newDeviceSettingsLayout(data *shellDataService, logger *slog.Logger) func(r *http.Request, content templ.Component) templ.Component {
	return newRequestAdminAwareLayout(data, devicesPageTitle, logger)
}

// newStorageLayout returns the layout func injected into
// storageadapter.NewBinsWebHandlers/NewLocationsWebHandlers (see
// newRequestAdminAwareLayout's own doc). title is fixed per handler group
// (BinsWebHandlers' own routes always render "All bins" as the shell
// title, LocationsWebHandlers' own "Locations") since neither varies it
// per specific bin/location the way, say, a per-item title would.
func newStorageLayout(data *shellDataService, title string, logger *slog.Logger) func(r *http.Request, content templ.Component) templ.Component {
	return newRequestAdminAwareLayout(data, title, logger)
}

// shellNav is the sidebar's primary navigation, active-highlighted by path
// (NSTR-31): "All bins" for /bins and every bin detail page (/b/{code}...),
// "Locations" for /locations and its own detail/edit pages, an exact/
// prefix match for everything else. The three not-yet-built pages
// (Search & find, Categories, Labels & codes) still link out so the full
// nav renders and is reachable per the AC; each gets a real handler
// alongside the feature it belongs to. The Users entry (NSTR-21) only
// renders for an admin.
func shellNav(path string, isAdmin bool) []components.NavItem {
	nav := []components.NavItem{
		{Label: binsPageTitle, Href: "/bins", Active: navActive(path, "/bins") || navActive(path, "/b"), Icon: components.IconBin(shellIconClass)},
		{Label: searchPageTitle, Href: "/search", Active: navActive(path, "/search"), Icon: components.IconSearch(shellIconClass)},
		{Label: "Categories", Href: "/categories", Active: navActive(path, "/categories"), Icon: components.IconCategories(shellIconClass)},
		{Label: locationsPageTitle, Href: "/locations", Active: navActive(path, "/locations"), Icon: components.IconLocations(shellIconClass)},
		{Label: "Labels & codes", Href: "/labels", Active: navActive(path, "/labels"), Icon: components.IconLabels(shellIconClass)},
	}
	if isAdmin {
		nav = append(nav, components.NavItem{Label: usersPageTitle, Href: "/admin/users", Active: navActive(path, "/admin/users"), Icon: components.IconUsers(shellIconClass)})
	}
	return nav
}

// navActive reports whether path belongs under href: an exact match, or a
// path nested under it (href followed by "/"). "/b" is passed as href for a
// bin detail page (/b/{code}, /b/{code}/edit), which does not sit under
// "/bins" itself.
func navActive(path, href string) bool {
	return path == href || strings.HasPrefix(path, href+"/")
}
