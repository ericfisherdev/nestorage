package adapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestcore/render"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/notify/app"
	"github.com/ericfisherdev/nestorage/internal/notify/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
	"github.com/ericfisherdev/nestorage/web/components"
)

// preferencesWebService is the narrow port (ISP) PreferencesWebHandlers
// depends on, satisfied by *app.PreferenceService.
type preferencesWebService interface {
	PreferencesForUser(ctx context.Context, userID identity.UserID) ([]app.EventTypeSection, error)
	SetEmailEnabled(ctx context.Context, userID identity.UserID, eventType domain.EventType, enabled bool) error
}

// PreferencesWebHandlers serves the signed-in user's own notification
// settings screen (GET /settings/notifications) and lets them toggle email
// for one event type (POST /settings/notifications/{event}/email).
// Mirrors identity/adapter.DeviceTokenWebHandlers exactly: a narrow service
// interface (ISP), an injected requestLayoutFunc, the session manager for
// CSRF on the one mutating route, a logger. Every route is registered on
// its own mux, mounted behind RequireUser by the composition root — no
// admin role is required: a user manages only their OWN preferences,
// scoped by CurrentUser's id, never a path parameter.
type PreferencesWebHandlers struct {
	prefs  preferencesWebService
	sm     *scs.SessionManager
	layout requestLayoutFunc
	logger *slog.Logger
}

// NewPreferencesWebHandlers constructs PreferencesWebHandlers. All
// dependencies are required; a missing one panics at construction time,
// matching every other WebHandlers constructor in this codebase.
func NewPreferencesWebHandlers(prefs preferencesWebService, sm *scs.SessionManager, layout requestLayoutFunc, logger *slog.Logger) *PreferencesWebHandlers {
	if prefs == nil {
		panic("notify/adapter: NewPreferencesWebHandlers requires a non-nil preferencesWebService")
	}
	if sm == nil {
		panic("notify/adapter: NewPreferencesWebHandlers requires a non-nil session manager")
	}
	if layout == nil {
		panic("notify/adapter: NewPreferencesWebHandlers requires a non-nil layout func")
	}
	if logger == nil {
		panic("notify/adapter: NewPreferencesWebHandlers requires a non-nil logger")
	}
	return &PreferencesWebHandlers{prefs: prefs, sm: sm, layout: layout, logger: logger}
}

// Routes registers the notification settings routes on mux. mux is
// expected to be mounted behind RequireUser by the caller — see this
// type's own doc.
func (h *PreferencesWebHandlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /settings/notifications", h.Settings)
	mux.HandleFunc("POST /settings/notifications/{event}/email", h.SetEmailEnabled)
}

// Settings handles GET /settings/notifications.
func (h *PreferencesWebHandlers) Settings(w http.ResponseWriter, r *http.Request) {
	h.renderSettings(w, r, http.StatusOK)
}

// SetEmailEnabled handles POST /settings/notifications/{event}/email: CSRF,
// then toggle scoped to the session user — PreferenceService's own userID
// parameter is what makes toggling another user's preference impossible
// here, not this handler's own logic. enabled reflects whether the
// checkbox was present in the submitted form: an unchecked checkbox sends
// no field at all, the standard HTML forms contract.
func (h *PreferencesWebHandlers) SetEmailEnabled(w http.ResponseWriter, r *http.Request) {
	eventType, ok := h.pathEventType(w, r)
	if !ok {
		return
	}
	if !verifyRequest(w, r, h.sm) {
		return
	}

	user, ok := identityadapter.CurrentUser(r.Context())
	if !ok {
		// Unreachable behind RequireUser (see this type's own doc), guarded
		// anyway so a future mounting mistake fails safe rather than
		// panicking on a nil user.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	enabled := r.FormValue("enabled") != ""
	if err := h.prefs.SetEmailEnabled(r.Context(), user.ID, eventType, enabled); err != nil {
		h.handleMutationError(w, r, err)
		return
	}
	h.finishMutation(w, r)
}

// pathEventType parses the {event} path value, answering 400 and reporting
// ok=false on an unrecognized one — mirrors pathDeviceTokenID's own shape.
func (h *PreferencesWebHandlers) pathEventType(w http.ResponseWriter, r *http.Request) (domain.EventType, bool) {
	eventType, err := domain.ParseEventType(r.PathValue("event"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return "", false
	}
	return eventType, true
}

// finishMutation completes a successful toggle: an HTMX request gets the
// re-rendered settings fragment, a full navigation gets redirected to
// /settings/notifications (so a page refresh does not resubmit the form) —
// mirrors DeviceTokenWebHandlers.finishMutation's identical shape.
func (h *PreferencesWebHandlers) finishMutation(w http.ResponseWriter, r *http.Request) {
	if render.IsHTMX(r) {
		h.renderSettings(w, r, http.StatusOK)
		return
	}
	http.Redirect(w, r, "/settings/notifications", http.StatusSeeOther)
}

// handleMutationError maps a recognized domain error to its status, or logs
// and answers a generic 500 for anything else.
func (h *PreferencesWebHandlers) handleMutationError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrInvalidEventType) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	h.logger.ErrorContext(r.Context(), "notify: set email preference", "error", err)
	http.Error(w, errInternalServerError, http.StatusInternalServerError)
}

// renderSettings loads the signed-in user's preferences and renders the
// screen at status — the shared tail of Settings and every
// SetEmailEnabled outcome.
func (h *PreferencesWebHandlers) renderSettings(w http.ResponseWriter, r *http.Request, status int) {
	user, ok := identityadapter.CurrentUser(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sections, err := h.prefs.PreferencesForUser(r.Context(), user.ID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "notify: load preferences", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	view := h.buildView(r.Context(), sections)
	h.renderPage(w, r, status, view)
}

// renderPage renders view at status: the bare fragment for an HX-Request,
// or the full shell page for a normal navigation.
func (h *PreferencesWebHandlers) renderPage(w http.ResponseWriter, r *http.Request, status int, view components.NotificationSettingsView) {
	w.Header().Set("Vary", "HX-Request")
	content := components.NotificationSettings(view)
	c := content
	if !render.IsHTMX(r) {
		c = h.layout(r, content)
	}
	if err := render.Render(r.Context(), w, status, c); err != nil {
		h.logger.ErrorContext(r.Context(), "notify: render settings", "error", err)
	}
}

// buildView maps PreferenceService.PreferencesForUser's result into the
// view-layer model NotificationSettings renders.
func (h *PreferencesWebHandlers) buildView(ctx context.Context, sections []app.EventTypeSection) components.NotificationSettingsView {
	rows := make([]components.EventSection, 0, len(sections))
	for _, s := range sections {
		rows = append(rows, components.EventSection{
			EventType:    s.EventType.String(),
			Label:        eventTypeLabel(s.EventType),
			EmailEnabled: s.EmailEnabled,
		})
	}
	return components.NotificationSettingsView{
		CSRFToken: session.CSRFToken(ctx, h.sm),
		Sections:  rows,
	}
}

// eventTypeLabel renders et's human-readable heading for the settings
// screen.
func eventTypeLabel(et domain.EventType) string {
	switch et {
	case domain.EventTypeReturnRequested:
		return "Return requested"
	case domain.EventTypeItemReturned:
		return "Item returned"
	default:
		return et.String()
	}
}
