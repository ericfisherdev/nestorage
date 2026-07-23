package adapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestcore/render"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
	"github.com/ericfisherdev/nestorage/web/components"
)

// deviceTokenDateFormat is how a device row's CreatedAt/LastUsedAt render —
// formatted here in the handler, never in the template (components.DeviceView
// carries pre-formatted strings, never a time.Time).
const deviceTokenDateFormat = "Jan 2, 2006"

// deviceTokenWebService is the narrow port (ISP) DeviceTokenWebHandlers
// depends on, satisfied by *app.DeviceTokenService.
type deviceTokenWebService interface {
	ListForUser(ctx context.Context, userID domain.UserID) ([]*domain.DeviceToken, error)
	Revoke(ctx context.Context, userID domain.UserID, id domain.DeviceTokenID) error
}

// requestLayoutFunc wraps page content in the app shell for a specific
// request — unlike layoutFunc, it needs r to decide request-dependent shell
// state. DeviceTokenWebHandlers needs it (rather than plain layoutFunc)
// because, unlike NSTR-21's admin screen, this one is reachable by any
// signed-in user, not only an admin: shellNav's Users entry must still
// reflect the ACTUAL signed-in user, so the composition root's layout
// closure has to inspect r. Injected by the composition root, same
// rationale as layoutFunc: this package has no access to
// ShellProps/shellNav, which are cmd/server's own concepts.
type requestLayoutFunc func(r *http.Request, content templ.Component) templ.Component

// DeviceTokenWebHandlers serves the signed-in user's own device list
// (GET /settings/devices) and lets them revoke one of their own devices
// (POST /settings/devices/{id}/revoke). Every route is registered on its
// own mux, mounted behind RequireUser by the composition root — unlike
// NSTR-21's admin user management, no admin role is required: a user
// manages only their OWN devices, scoped by CurrentUser's id, never a path
// parameter.
type DeviceTokenWebHandlers struct {
	devices deviceTokenWebService
	sm      *scs.SessionManager
	layout  requestLayoutFunc
	logger  *slog.Logger
}

// NewDeviceTokenWebHandlers constructs DeviceTokenWebHandlers. All
// dependencies are required; a missing one panics at construction time,
// matching every other WebHandlers constructor in this codebase.
func NewDeviceTokenWebHandlers(devices deviceTokenWebService, sm *scs.SessionManager, layout requestLayoutFunc, logger *slog.Logger) *DeviceTokenWebHandlers {
	if devices == nil {
		panic("identity/adapter: NewDeviceTokenWebHandlers requires a non-nil deviceTokenWebService")
	}
	if sm == nil {
		panic("identity/adapter: NewDeviceTokenWebHandlers requires a non-nil session manager")
	}
	if layout == nil {
		panic("identity/adapter: NewDeviceTokenWebHandlers requires a non-nil layout func")
	}
	if logger == nil {
		panic("identity/adapter: NewDeviceTokenWebHandlers requires a non-nil logger")
	}
	return &DeviceTokenWebHandlers{devices: devices, sm: sm, layout: layout, logger: logger}
}

// Routes registers the device self-service routes on mux. mux is expected to
// be mounted behind RequireUser by the caller — see this type's own doc.
func (h *DeviceTokenWebHandlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /settings/devices", h.List)
	mux.HandleFunc("POST /settings/devices/{id}/revoke", h.Revoke)
}

// List handles GET /settings/devices.
func (h *DeviceTokenWebHandlers) List(w http.ResponseWriter, r *http.Request) {
	h.renderList(w, r, http.StatusOK)
}

// Revoke handles POST /settings/devices/{id}/revoke: CSRF, then revoke
// scoped to the session user — DeviceTokenService.Revoke's own userID
// parameter is what makes revoking another user's device impossible here,
// not this handler's own logic.
func (h *DeviceTokenWebHandlers) Revoke(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathDeviceTokenID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !session.VerifyCSRF(r, h.sm) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}

	user, ok := CurrentUser(r.Context())
	if !ok {
		// Unreachable behind RequireUser (see this type's own doc), guarded
		// anyway so a future mounting mistake fails safe rather than
		// panicking on a nil user.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := h.devices.Revoke(r.Context(), user.ID, id); err != nil {
		h.handleMutationError(w, r, err)
		return
	}
	h.finishMutation(w, r)
}

// pathDeviceTokenID parses the {id} path value, answering 400 and reporting
// ok=false on a malformed one.
func (h *DeviceTokenWebHandlers) pathDeviceTokenID(w http.ResponseWriter, r *http.Request) (domain.DeviceTokenID, bool) {
	id, err := domain.ParseDeviceTokenID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return domain.DeviceTokenID{}, false
	}
	return id, true
}

// finishMutation completes a successful revoke: an HTMX request gets the
// re-rendered list fragment, a full navigation gets redirected to
// /settings/devices (so a page refresh does not resubmit the form).
func (h *DeviceTokenWebHandlers) finishMutation(w http.ResponseWriter, r *http.Request) {
	if render.IsHTMX(r) {
		h.renderList(w, r, http.StatusOK)
		return
	}
	http.Redirect(w, r, "/settings/devices", http.StatusSeeOther)
}

// handleMutationError maps a recognized domain error to its status, or logs
// and answers a generic 500 for anything else.
func (h *DeviceTokenWebHandlers) handleMutationError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrDeviceTokenNotFound) {
		http.Error(w, "that device no longer exists", http.StatusNotFound)
		return
	}
	h.logger.ErrorContext(r.Context(), "devices: mutate", "error", err)
	http.Error(w, errInternalServerError, http.StatusInternalServerError)
}

// renderList loads the signed-in user's devices and renders the screen at
// status — the shared tail of List and every Revoke outcome.
func (h *DeviceTokenWebHandlers) renderList(w http.ResponseWriter, r *http.Request, status int) {
	user, ok := CurrentUser(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	tokens, err := h.devices.ListForUser(r.Context(), user.ID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "devices: list", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	view := h.buildView(r.Context(), tokens)
	h.renderPage(w, r, status, view)
}

// renderPage renders view at status: the bare list fragment for an
// HX-Request, or the full shell page for a normal navigation.
func (h *DeviceTokenWebHandlers) renderPage(w http.ResponseWriter, r *http.Request, status int, view components.DevicesView) {
	w.Header().Set("Vary", "HX-Request")
	content := components.DeviceList(view)
	c := content
	if !render.IsHTMX(r) {
		c = h.layout(r, content)
	}
	if err := render.Render(r.Context(), w, status, c); err != nil {
		h.logger.ErrorContext(r.Context(), "devices: render", "error", err)
	}
}

// buildView maps DeviceTokenService.ListForUser's result into the
// view-layer model DeviceList renders, formatting every timestamp here so
// components.DeviceView never carries a time.Time.
func (h *DeviceTokenWebHandlers) buildView(ctx context.Context, tokens []*domain.DeviceToken) components.DevicesView {
	rows := make([]components.DeviceView, 0, len(tokens))
	for _, t := range tokens {
		rows = append(rows, components.DeviceView{
			ID:         t.ID.String(),
			Name:       t.Name,
			CreatedAt:  t.CreatedAt.Format(deviceTokenDateFormat),
			LastUsedAt: formatLastUsed(t.LastUsedAt),
		})
	}
	return components.DevicesView{
		CSRFToken: session.CSRFToken(ctx, h.sm),
		Devices:   rows,
	}
}

// formatLastUsed renders "Never" for a token that has not authenticated
// since it was issued, or the formatted date otherwise.
func formatLastUsed(t *time.Time) string {
	if t == nil {
		return "Never"
	}
	return t.Format(deviceTokenDateFormat)
}
