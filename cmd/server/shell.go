package main

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/ericfisherdev/nestcore/httpserver"
	"github.com/ericfisherdev/nestcore/httpserver/middleware"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	labelsadapter "github.com/ericfisherdev/nestorage/internal/labels/adapter"
	labelsdomain "github.com/ericfisherdev/nestorage/internal/labels/domain"
	mediaadapter "github.com/ericfisherdev/nestorage/internal/media/adapter"
	notifyadapter "github.com/ericfisherdev/nestorage/internal/notify/adapter"
	"github.com/ericfisherdev/nestorage/internal/platform/api"
	ratelimitmetrics "github.com/ericfisherdev/nestorage/internal/platform/metrics"
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

// notificationsPageTitle names NSTR-44's own /notifications inbox page.
const notificationsPageTitle = "Notifications"

// labelsPrintPageTitle names NSTR-51's own /labels/print screen — a
// distinct string from the sidebar's "Labels & codes" nav entry (still a
// placeholder link to a not-yet-built /labels page, per shellNav's own
// doc), since this ticket's screen is reached from a bin/location detail
// page's own toolbar, never that nav entry.
const labelsPrintPageTitle = "Print labels"

// notificationSettingsPageTitle names NSTR-45's own /settings/notifications
// preferences page — a distinct string from notificationsPageTitle so the
// two pages' own shell titles never collide, even though both live under
// the same "notify" bounded context.
const notificationSettingsPageTitle = "Notification settings"

// passwordSettingsPageTitle names NSTR-103's own /settings/password
// self-service password-change page.
const passwordSettingsPageTitle = "Password"

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
//
// labels holds NSTR-47's label size registry: it is not behind a narrow
// port the way members/bins/locations are (nothing here calls into it yet),
// but the composition root builds exactly one *labelsdomain.Registry at
// boot (see cmd/server/main.go), and shellDataService is where every other
// process-wide, request-independent composition value already lives for
// NSTR-50's batch service and NSTR-51's size-selection UI to read back via
// Labels().
type shellDataService struct {
	members   shellMemberLister
	bins      shellBinLister
	locations shellLocationLister
	labels    *labelsdomain.Registry
}

// newShellDataService constructs shellDataService. All dependencies are
// required; a missing one panics at construction time, matching every other
// constructor in this codebase.
func newShellDataService(members shellMemberLister, bins shellBinLister, locations shellLocationLister, labels *labelsdomain.Registry) *shellDataService {
	if members == nil {
		panic("main: newShellDataService requires a non-nil shellMemberLister")
	}
	if bins == nil {
		panic("main: newShellDataService requires a non-nil shellBinLister")
	}
	if locations == nil {
		panic("main: newShellDataService requires a non-nil shellLocationLister")
	}
	if labels == nil {
		panic("main: newShellDataService requires a non-nil *labelsdomain.Registry")
	}
	return &shellDataService{members: members, bins: bins, locations: locations, labels: labels}
}

// Labels returns the label size registry NSTR-50's batch service and
// NSTR-51's size-selection UI read from — the same registry the
// composition root constructed once, at boot (see cmd/server/main.go),
// never a per-request rebuild.
func (s *shellDataService) Labels() *labelsdomain.Registry {
	return s.labels
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
// mounted at the top level alongside login, matching its own doc. NSTR-45's
// own /settings/notifications... routes join the SAME settingsMux for the
// identical reason — any signed-in user manages only their own notification
// preferences, not an admin concern.
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
// specific pattern claimed. NSTR-37's own item photo routes
// (/items/{id}/photos...) join the SAME storageMux (Sprint 5
// reconciliation R3, which supersedes that ticket's own plan of mounting a
// second mux on "/" — a second "/" registration panics at startup); their
// patterns do not collide with /items/{id} or NSTR-38's own
// /items/{id}/links... patterns. NSTR-44's own /notifications... routes join
// the same storageMux for the identical reason — any signed-in household
// member's own inbox, not an admin concern — and share no prefix with any
// bin/location/item/photo pattern already registered on it.
//
// NSTR-53's own /api/v1 mount (newAPIRouteMount) is registered at
// "/api/v1/", behind RequireAPICredential rather than any of the
// session-based gates above — a bearer credential only, JSON-only surface.
// It sits alongside deps.DeviceTokenAPI.Routes(rateLimitedRegistrar{...})
// and deps.SpecAPI.Routes(mux), both registered at the top level just
// above: "POST /api/v1/auth/device-tokens" and "GET /api/v1/openapi.yaml"
// are each more specific patterns than the "/api/v1/" subtree, so
// net/http.ServeMux keeps routing them there, outside the bearer gate — the
// credential-minting endpoint has to stay reachable with no credential yet
// to present, and NSTR-57's own published spec has to stay reachable by a
// client that has not obtained one yet either. NSTR-58 additionally wraps
// the device-token registration in rateLimitedRegistrar (authWrap) so that
// one route — alone among everything mounted outside the /api/v1 gate —
// still sits behind rate limiting; deps.SpecAPI is deliberately left
// unwrapped (a static document, safe to leave unlimited).
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
	PreferencesWeb *notifyadapter.PreferencesWebHandlers
	PasswordWeb    *identityadapter.PasswordWebHandlers
	APIKeyWeb      *identityadapter.APIKeyWebHandlers
	Bins           *storageadapter.BinsWebHandlers
	Locations      *storageadapter.LocationsWebHandlers
	Labels         *labelsadapter.LabelsWebHandlers
	Items          *storageadapter.ItemsWebHandlers
	Photos         *mediaadapter.PhotosWebHandlers
	Notifications  *notifyadapter.InboxWebHandlers
	Denier         *identityadapter.Denier
	APIMetrics     *api.Metrics
	// LocationsAPI, BinsAPI, and ItemsAPI are NSTR-54's own /api/v1 CRUD
	// surface — registered on the api.Router newAPIRouteMount builds, never
	// on storageMux above: they carry no session and answer through the
	// shared JSON envelope, not templ/HTMX.
	LocationsAPI *storageadapter.LocationsAPIHandlers
	BinsAPI      *storageadapter.BinsAPIHandlers
	ItemsAPI     *storageadapter.ItemsAPIHandlers
	// OperationsAPI and HistoryAPI are NSTR-55's own /api/v1 surface: the
	// item/bin state-transition endpoints and the two event-history reads,
	// mounted on the exact same api.Router as LocationsAPI/BinsAPI/ItemsAPI
	// above.
	OperationsAPI *storageadapter.OperationsAPIHandlers
	HistoryAPI    *storageadapter.HistoryAPIHandlers
	// PhotosAPI is NSTR-56's own /api/v1 surface: item photo upload/list/
	// serve/set-primary/delete, mounted on the same api.Router — a thin
	// JSON/multipart adapter over the exact same photoOperator (mediaapp.
	// PhotoService) the Photos web handlers above already share, so the API
	// and the web gallery can never disagree on validation, EXIF scrubbing,
	// or storage.
	PhotosAPI *mediaadapter.PhotosAPIHandlers
	// FederationAPI is NSTR-101's own /api/v1 surface: the account-read
	// reconciliation feeds on, and the link/provision endpoint
	// NSTR-106/107/108's attach and re-push calls drive. Both of its routes
	// require the account api key (requireIntegrationPrincipal) rather than
	// rejecting it the way every create endpoint above does, so it is
	// mounted on the same api.Router alongside every other bearer-gated
	// group, not treated as a special case.
	FederationAPI *identityadapter.FederationAPIHandlers
	// SpecAPI is NSTR-57's own route: the published OpenAPI 3.1 document,
	// GET /api/v1/openapi.yaml. Registered on the outer mux alongside
	// DeviceTokenAPI, NOT inside registerAPIRoutes below — the spec is
	// public (the repository is public) even though the data behind it is
	// not, matching DeviceTokenAPI's own reason for sitting outside the
	// bearer gate.
	SpecAPI *api.SpecHandlers
	// APILimiter is NSTR-58's own account-wide bucket, wrapping the whole
	// /api/v1 mount (newAPIRouteMount): RateLimit(APILimiter,
	// PrincipalRateKey, "api"). AuthLimiter is the additional, stricter
	// bucket over the token-exchange route alone: RateLimit(AuthLimiter,
	// ClientIPRateKey, "auth") — the exchange route sits behind BOTH (see
	// newAppRoutes' own doc), so whichever budget is tighter is what a
	// caller observes (AC 3). RateLimitMetrics is the shared recorder both
	// wrap into their own RateLimit call.
	APILimiter       *identityadapter.KeyedRateLimiter
	AuthLimiter      *identityadapter.KeyedRateLimiter
	RateLimitMetrics *ratelimitmetrics.RateLimitMetrics
}

// registerAPIRoutes mounts NSTR-54/55/56's bearer-gated JSON API route
// groups onto reg — Locations/Bins/Items CRUD, the item/bin operation and
// history endpoints, and item photos. This is the exact closure newAppRoutes
// wraps behind newAPIRouteMount's RequireAPICredential gate, factored into
// its own named function so NSTR-57's route/spec sync test
// (cmd/server/apispec_test.go) builds the identical route set production
// does, through the identical call sequence, rather than risking the two
// drifting apart. deps.DeviceTokenAPI and deps.SpecAPI are deliberately NOT
// included here — see appRouteDeps' own doc for why their routes are
// mounted outside this gate.
func registerAPIRoutes(deps appRouteDeps) func(api.Registrar) {
	return func(reg api.Registrar) {
		deps.LocationsAPI.Routes(reg)
		deps.BinsAPI.Routes(reg)
		deps.ItemsAPI.Routes(reg)
		deps.OperationsAPI.Routes(reg)
		deps.HistoryAPI.Routes(reg)
		deps.PhotosAPI.Routes(reg)
		deps.FederationAPI.Routes(reg)
	}
}

func newAppRoutes(deps appRouteDeps) func(mux *http.ServeMux) {
	shell := newShellHandlers(deps.Logger)
	adminGate := identityadapter.RequireAdmin(deps.Denier)
	userGate := identityadapter.RequireUser()
	authGate := identityadapter.RequireAuthenticated(deps.Denier)
	// NSTR-58's own token-exchange rate limiting: DeviceTokenAPI.Routes'
	// registration is wrapped through rateLimitedRegistrar (below) rather
	// than called on mux directly, so the route it registers additionally sits
	// behind BOTH the account-wide api bucket and the stricter auth bucket
	// (appRouteDeps.AuthLimiter's own doc) — without duplicating
	// "POST /api/v1/auth/device-tokens" as a second pattern string
	// alongside DeviceTokenAPIHandlers.Routes' own single registration.
	authWrap := middleware.Chain(
		identityadapter.RateLimit(deps.APILimiter, identityadapter.PrincipalRateKey, "api", deps.RateLimitMetrics, newRateLimitErrorWriter(deps.Logger), time.Now),
		identityadapter.RateLimit(deps.AuthLimiter, identityadapter.ClientIPRateKey, "auth", deps.RateLimitMetrics, newRateLimitErrorWriter(deps.Logger), time.Now),
	)
	return func(mux *http.ServeMux) {
		shell.Routes(mux)
		deps.Onboarding.Routes(mux)
		deps.Login.Routes(mux)
		deps.DeviceTokenAPI.Routes(rateLimitedRegistrar{Registrar: mux, wrap: authWrap})
		deps.SpecAPI.Routes(mux)
		mux.Handle("/api/v1/", newAPIRouteMount(deps.Denier, deps.APIMetrics, deps.APILimiter, deps.RateLimitMetrics, deps.Logger, registerAPIRoutes(deps)))

		adminMux := http.NewServeMux()
		deps.Users.Routes(adminMux)
		mux.Handle("/admin/", adminGate(adminMux))

		settingsMux := http.NewServeMux()
		deps.DeviceTokenWeb.Routes(settingsMux)
		deps.PreferencesWeb.Routes(settingsMux)
		deps.PasswordWeb.Routes(settingsMux)
		mux.Handle("/settings/", userGate(settingsMux))

		apiKeyMux := http.NewServeMux()
		deps.APIKeyWeb.Routes(apiKeyMux)
		gatedAPIKey := adminGate(apiKeyMux)
		mux.Handle("/settings/api-key", gatedAPIKey)
		mux.Handle("/settings/api-key/", gatedAPIKey)

		storageMux := http.NewServeMux()
		deps.Bins.Routes(storageMux)
		deps.Locations.Routes(storageMux)
		// NSTR-50's GET /locations/{id}/labels.pdf is more specific than
		// deps.Locations' own GET /locations/{id}, so net/http.ServeMux
		// routes a request to the right handler with no conflict between
		// the two registrations sharing this mux.
		deps.Labels.Routes(storageMux)
		deps.Items.Routes(storageMux)
		deps.Photos.Routes(storageMux)
		deps.Notifications.Routes(storageMux)
		mux.Handle("/", authGate(storageMux))
	}
}

// newAPIRouteMount builds the /api/v1 handler newAppRoutes mounts at
// "/api/v1/": an api.Router every bounded context's own API adapter mounts
// its routes onto through Handle/HandleFunc (api.Registrar) — registerRoutes
// is called once, before the router is wrapped, so NSTR-54's own
// Locations/Bins/ItemsAPIHandlers (and any later context's own API adapter)
// register onto the exact same api.Router instance that answers the JSON
// 404/405 fallback — wrapped with RequireAPICredential's bearer gate and
// api.Observe's own instrumentation, itself wrapped in NSTR-58's own
// account-wide RateLimit (apiLimiter, PrincipalRateKey, "api") — the
// OUTERMOST layer, checked before Observe/RequireAPICredential/routing ever
// run, so a limited request is turned away as cheaply as possible and never
// reaches (or skews) the request-count/latency instrumentation those inner
// layers record. Observe itself sits OUTSIDE the credential gate so a
// denied-but-not-limited request is still counted, with route falling back
// to the mount pattern since no inner route ran (see Observe's own doc).
// Split out of newAppRoutes so it is unit-testable without newAppRoutes'
// many other concrete dependencies — a test passes its own registerRoutes
// (or a no-op) rather than every bounded context's real handlers.
func newAPIRouteMount(denier *identityadapter.Denier, apiMetrics *api.Metrics, apiLimiter *identityadapter.KeyedRateLimiter, rateLimitMetrics *ratelimitmetrics.RateLimitMetrics, logger *slog.Logger, registerRoutes func(api.Registrar)) http.Handler {
	apiRouter := api.NewRouter(logger)
	registerRoutes(apiRouter)
	return identityadapter.RateLimit(apiLimiter, identityadapter.PrincipalRateKey, "api", rateLimitMetrics, newRateLimitErrorWriter(logger), time.Now)(
		api.Observe(apiMetrics, identityadapter.PrincipalKindLabel, logger)(
			identityadapter.RequireAPICredential(denier)(apiRouter.Handler()),
		),
	)
}

// rateLimitedRegistrar wraps an api.Registrar so every route registered
// through it (via HandleFunc, DeviceTokenAPIHandlers.Routes' own only call)
// picks up wrap before reaching the underlying Registrar — see
// newAppRoutes' own doc for why the token-exchange route needs this rather
// than composing RateLimit at newAPIRouteMount's level the way the rest of
// /api/v1 does (DeviceTokenAPI is deliberately registered OUTSIDE that
// mount, unauthenticated).
type rateLimitedRegistrar struct {
	api.Registrar
	wrap middleware.Middleware
}

// Handle wraps handler in wrap before delegating to the underlying
// Registrar — completes api.Registrar alongside HandleFunc below, even
// though DeviceTokenAPIHandlers.Routes (this type's only production caller)
// exercises HandleFunc alone.
func (r rateLimitedRegistrar) Handle(pattern string, handler http.Handler) {
	r.Registrar.Handle(pattern, r.wrap(handler))
}

// HandleFunc wraps handler in wrap before delegating to the underlying
// Registrar's own Handle.
func (r rateLimitedRegistrar) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	r.Registrar.Handle(pattern, r.wrap(http.HandlerFunc(handler)))
}

// newRateLimitErrorWriter closes over logger to satisfy identityadapter's
// own errorWriter port (RateLimit's own doc) with api.WriteError — NSTR-53's
// shared JSON error writer — so a 429 answers through the exact same
// envelope every other /api/v1 error in this codebase uses.
func newRateLimitErrorWriter(logger *slog.Logger) func(w http.ResponseWriter, status int, code api.Code, message string) {
	return func(w http.ResponseWriter, status int, code api.Code, message string) {
		api.WriteError(w, logger, status, code, message)
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

// newNotificationSettingsLayout returns the layout func injected into
// notifyadapter.NewPreferencesWebHandlers (see newRequestAdminAwareLayout's
// own doc) — NSTR-45's /settings/notifications page is reachable by any
// signed-in household member, not only an admin, the same shape
// newDeviceSettingsLayout already has.
func newNotificationSettingsLayout(data *shellDataService, logger *slog.Logger) func(r *http.Request, content templ.Component) templ.Component {
	return newRequestAdminAwareLayout(data, notificationSettingsPageTitle, logger)
}

// newPasswordSettingsLayout returns the layout func injected into
// identityadapter.NewPasswordWebHandlers (see newRequestAdminAwareLayout's
// own doc) — NSTR-103's /settings/password page is reachable by any
// signed-in household member, not only an admin, the same shellNav-
// reflects-the-actual-viewer shape newDeviceSettingsLayout already has.
func newPasswordSettingsLayout(data *shellDataService, logger *slog.Logger) func(r *http.Request, content templ.Component) templ.Component {
	return newRequestAdminAwareLayout(data, passwordSettingsPageTitle, logger)
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

// newLabelsPrintLayout returns the layout func injected into
// labelsadapter.NewLabelsWebHandlers (see newRequestAdminAwareLayout's own
// doc) — NSTR-51's /labels/print screen is reachable by any signed-in
// household member, not only an admin, the same shellNav-reflects-the-
// actual-viewer shape newStorageLayout already has.
func newLabelsPrintLayout(data *shellDataService, logger *slog.Logger) func(r *http.Request, content templ.Component) templ.Component {
	return newRequestAdminAwareLayout(data, labelsPrintPageTitle, logger)
}

// newNotificationsLayout returns the layout func injected into
// notifyadapter.NewInboxWebHandlers (see newRequestAdminAwareLayout's own
// doc) — NSTR-44's /notifications page is reachable by any signed-in
// household member, not only an admin, the same shellNav-reflects-the-
// actual-viewer shape newDeviceSettingsLayout/newStorageLayout already
// share.
func newNotificationsLayout(data *shellDataService, logger *slog.Logger) func(r *http.Request, content templ.Component) templ.Component {
	return newRequestAdminAwareLayout(data, notificationsPageTitle, logger)
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
