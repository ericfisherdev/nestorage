package adapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestcore/render"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
	"github.com/ericfisherdev/nestorage/web/components"
)

// currentPasswordIncorrectMessage is the 422 message a wrong current
// password (or, indistinguishably, an inactive user — see
// app.PasswordService.ChangeOwn's own doc) re-renders the form with.
const currentPasswordIncorrectMessage = "Current password is incorrect."

// passwordTooShortMessage and passwordTooLongMessage reuse mapAdminError's
// exact wording (users_web.go) so the same validation failure reads
// identically whether an admin reset it or a member changed it themselves.
const (
	passwordTooShortMessage = "Password must be at least 12 characters."
	passwordTooLongMessage  = "Password must be at most 128 characters."
)

// newPasswordDoesNotMatchMessage is the 422 message a mismatched
// confirmation field re-renders the form with — the same wording
// parseNewUserForm/ResetPassword already use (users_web.go) for the
// identically-shaped check.
const newPasswordDoesNotMatchMessage = "Passwords do not match."

// passwordChangedMessage is the confirmation shown after a successful
// change — the session that made the change survives it (see finishChange's
// own doc), so this message is the only visible sign anything happened.
const passwordChangedMessage = "Your password has been changed."

// passwordChanger is the narrow port (ISP) PasswordWebHandlers
// depends on, satisfied by *app.PasswordService.
type passwordChanger interface {
	ChangeOwn(ctx context.Context, id domain.UserID, currentPassword, newPassword string) error
}

// PasswordWebHandlers serves NSTR-103's self-service password-change
// screen: GET /settings/password and POST /settings/password, reachable by
// any signed-in household member (not only an admin) — the same
// self-service shape DeviceTokenWebHandlers already has for the devices
// screen.
type PasswordWebHandlers struct {
	passwords passwordChanger
	sm        *scs.SessionManager
	layout    requestLayoutFunc
	logger    *slog.Logger
}

// NewPasswordWebHandlers constructs PasswordWebHandlers. All dependencies
// are required; a missing one panics at construction time, matching every
// other WebHandlers constructor in this codebase.
func NewPasswordWebHandlers(passwords passwordChanger, sm *scs.SessionManager, layout requestLayoutFunc, logger *slog.Logger) *PasswordWebHandlers {
	if passwords == nil {
		panic("identity/adapter: NewPasswordWebHandlers requires a non-nil passwordChanger")
	}
	if sm == nil {
		panic("identity/adapter: NewPasswordWebHandlers requires a non-nil session manager")
	}
	if layout == nil {
		panic("identity/adapter: NewPasswordWebHandlers requires a non-nil layout func")
	}
	if logger == nil {
		panic("identity/adapter: NewPasswordWebHandlers requires a non-nil logger")
	}
	return &PasswordWebHandlers{passwords: passwords, sm: sm, layout: layout, logger: logger}
}

// Routes registers the password self-service routes on mux. mux is expected
// to be mounted behind RequireUser by the caller, alongside
// DeviceTokenWebHandlers/PreferencesWebHandlers — see this type's own doc.
func (h *PasswordWebHandlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /settings/password", h.Show)
	mux.HandleFunc("POST /settings/password", h.Change)
}

// Show handles GET /settings/password: renders the form with a fresh CSRF
// token.
func (h *PasswordWebHandlers) Show(w http.ResponseWriter, r *http.Request) {
	h.renderForm(w, r, http.StatusOK, "", "")
}

// Change handles POST /settings/password: verifyRequest's ParseForm+CSRF
// shape (mirrors UsersWebHandlers.verifyRequest, users_web.go), the session
// user (guarded 401 like DeviceTokenWebHandlers.Revoke), the confirmation
// match, then ChangeOwn.
func (h *PasswordWebHandlers) Change(w http.ResponseWriter, r *http.Request) {
	if !h.verifyRequest(w, r) {
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

	currentPassword := r.FormValue("current_password")
	newPassword := r.FormValue("new_password")
	if newPassword != r.FormValue("new_password_confirmation") {
		h.renderForm(w, r, http.StatusUnprocessableEntity, newPasswordDoesNotMatchMessage, "")
		return
	}

	if err := h.passwords.ChangeOwn(r.Context(), user.ID, currentPassword, newPassword); err != nil {
		h.handleChangeError(w, r, err)
		return
	}
	h.finishChange(w, r, user.ID)
}

// verifyRequest parses the form and verifies the CSRF token — the shape
// UsersWebHandlers.verifyRequest already establishes (users_web.go).
// Answers 400 or 403 and reports ok=false on failure.
func (h *PasswordWebHandlers) verifyRequest(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	if !session.VerifyCSRF(r, h.sm) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return false
	}
	return true
}

// finishChange completes a successful change: sm.RenewToken then re-Put of
// session.KeyUserID — the exact sequence Handlers.Login uses (web.go) — so
// the session that made the change survives ChangeOwn's own revocation
// under a fresh token, while every OTHER session/device token belonging to
// userID was just destroyed. An HTMX request gets the re-rendered form with
// a confirmation message; a full navigation gets redirected to
// /settings/password (so a page refresh does not resubmit the form).
func (h *PasswordWebHandlers) finishChange(w http.ResponseWriter, r *http.Request, userID domain.UserID) {
	if err := h.sm.RenewToken(r.Context()); err != nil {
		h.logger.ErrorContext(r.Context(), "password: renew session token", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	h.sm.Put(r.Context(), session.KeyUserID, userID.String())

	if render.IsHTMX(r) {
		h.renderForm(w, r, http.StatusOK, "", passwordChangedMessage)
		return
	}
	http.Redirect(w, r, "/settings/password", http.StatusSeeOther)
}

// handleChangeError maps a recognized domain error from ChangeOwn to the
// form's inline error state (mapPasswordChangeError), or logs and answers a
// generic 500 for anything else — mirrors
// UsersWebHandlers.handleMutationError's identical split.
func (h *PasswordWebHandlers) handleChangeError(w http.ResponseWriter, r *http.Request, err error) {
	if status, message, ok := mapPasswordChangeError(err); ok {
		h.renderForm(w, r, status, message, "")
		return
	}
	h.logger.ErrorContext(r.Context(), "password: change own", "error", err)
	http.Error(w, errInternalServerError, http.StatusInternalServerError)
}

// renderForm renders the standalone password-change form at status, with a
// fresh CSRF token and the given inline error/success message (at most one
// is ever non-empty).
func (h *PasswordWebHandlers) renderForm(w http.ResponseWriter, r *http.Request, status int, errMsg, successMsg string) {
	view := components.PasswordSettingsView{
		CSRFToken: session.CSRFToken(r.Context(), h.sm),
		Error:     errMsg,
		Success:   successMsg,
	}
	h.renderPage(w, r, status, view)
}

// renderPage renders view at status: the bare fragment for an HX-Request,
// or the full shell page for a normal navigation — mirrors
// DeviceTokenWebHandlers.renderPage's identical split.
func (h *PasswordWebHandlers) renderPage(w http.ResponseWriter, r *http.Request, status int, view components.PasswordSettingsView) {
	w.Header().Set("Vary", "HX-Request")
	content := components.PasswordSettings(view)
	c := content
	if !render.IsHTMX(r) {
		c = h.layout(r, content)
	}
	if err := render.Render(r.Context(), w, status, c); err != nil {
		h.logger.ErrorContext(r.Context(), "password: render", "error", err)
	}
}

// mapPasswordChangeError maps a domain error from ChangeOwn to the HTTP
// status and inline message the form re-renders with — mirrors
// mapAdminError's identical shape (users_web.go). ok reports whether err was
// recognized; an unrecognized error (including domain.ErrUserNotFound,
// which should not occur here since CurrentUser already implies a live
// session for an existing user) is the caller's cue to log it and answer a
// generic 500 instead.
func mapPasswordChangeError(err error) (status int, message string, ok bool) {
	switch {
	case errors.Is(err, domain.ErrInvalidCredentials):
		return http.StatusUnprocessableEntity, currentPasswordIncorrectMessage, true
	case errors.Is(err, domain.ErrPasswordTooShort):
		return http.StatusUnprocessableEntity, passwordTooShortMessage, true
	case errors.Is(err, domain.ErrPasswordTooLong):
		return http.StatusUnprocessableEntity, passwordTooLongMessage, true
	default:
		return 0, "", false
	}
}
