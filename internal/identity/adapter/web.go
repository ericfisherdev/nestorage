package adapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestcore/render"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
	"github.com/ericfisherdev/nestorage/web/components"
)

// invalidCredentialsMessage is the one generic message every failed login
// re-renders with — unknown email, wrong password, a locked-out email, and
// an inactive user are all indistinguishable from outside.
const invalidCredentialsMessage = "Invalid email or password."

// loginAuthenticator is the narrow port (ISP) Handlers depends on for
// credential verification, satisfied by *app.Authenticator.
type loginAuthenticator interface {
	Login(ctx context.Context, email, password string) (domain.UserID, error)
}

// Handlers serves the session-cookie login and logout routes.
type Handlers struct {
	sm      *scs.SessionManager
	authn   loginAuthenticator
	limiter *LoginAttemptLimiter
	logger  *slog.Logger
}

// NewHandlers constructs Handlers. limiter is injected rather than
// constructed internally so the composition root can share one
// LoginAttemptLimiter with NSTR-22's device-token exchange endpoint — see
// LoginAttemptLimiter's own doc for why that sharing matters. All
// dependencies are required; a missing one panics at construction time,
// matching every other WebHandlers constructor in this codebase.
func NewHandlers(sm *scs.SessionManager, authn loginAuthenticator, limiter *LoginAttemptLimiter, logger *slog.Logger) *Handlers {
	if sm == nil {
		panic("identity/adapter: NewHandlers requires a non-nil session manager")
	}
	if authn == nil {
		panic("identity/adapter: NewHandlers requires a non-nil loginAuthenticator")
	}
	if limiter == nil {
		panic("identity/adapter: NewHandlers requires a non-nil LoginAttemptLimiter")
	}
	if logger == nil {
		panic("identity/adapter: NewHandlers requires a non-nil logger")
	}
	return &Handlers{sm: sm, authn: authn, limiter: limiter, logger: logger}
}

// Routes registers the login and logout routes on mux.
func (h *Handlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", h.LoginPage)
	mux.HandleFunc("POST /login", h.Login)
	mux.HandleFunc("POST /logout", h.Logout)
}

// LoginPage handles GET /login: it renders the login form with a fresh CSRF
// token and an optional next redirect target.
func (h *Handlers) LoginPage(w http.ResponseWriter, r *http.Request) {
	token := session.CSRFToken(r.Context(), h.sm)
	next := sanitizeNext(r.URL.Query().Get("next"))
	h.render(w, r, http.StatusOK, components.LoginForm{CSRFToken: token, Next: next})
}

// Login handles POST /login: CSRF, the attempt limiter, credential
// verification, and — on success — establishing the session. The limiter is
// checked BEFORE the Authenticator is touched at all, so a locked-out email
// never reaches the hasher.
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !session.VerifyCSRF(r, h.sm) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}

	email := domain.NormalizeEmail(r.FormValue("email"))
	password := r.FormValue("password")
	next := sanitizeNext(r.FormValue("next"))

	if h.limiter.Locked(email, time.Now()) {
		h.renderInvalidCredentials(w, r, email, next)
		return
	}

	userID, err := h.authn.Login(r.Context(), email, password)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			if h.limiter.RecordFailure(email, time.Now()) {
				h.logger.WarnContext(r.Context(), "login: account locked out after repeated failures")
			}
			h.renderInvalidCredentials(w, r, email, next)
			return
		}
		h.logger.ErrorContext(r.Context(), "login: authenticate", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	h.limiter.RecordSuccess(email)

	// Session-fixation defense: renew the token before storing any
	// privilege in the session.
	if err := h.sm.RenewToken(r.Context()); err != nil {
		h.logger.ErrorContext(r.Context(), "login: renew session token", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	h.sm.Put(r.Context(), session.KeyUserID, userID.String())
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// Logout handles POST /logout: it verifies the CSRF token, destroys the
// session server-side (not just the cookie), and redirects to /login.
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !session.VerifyCSRF(r, h.sm) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := h.sm.Destroy(r.Context()); err != nil {
		h.logger.ErrorContext(r.Context(), "logout: destroy session", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// renderInvalidCredentials re-renders the login form at 401 with the one
// generic message shared by every failure mode.
func (h *Handlers) renderInvalidCredentials(w http.ResponseWriter, r *http.Request, email, next string) {
	token := session.CSRFToken(r.Context(), h.sm)
	h.render(w, r, http.StatusUnauthorized, components.LoginForm{
		CSRFToken: token,
		Next:      next,
		Email:     email,
		Error:     invalidCredentialsMessage,
	})
}

// render renders the login page at the given status code.
func (h *Handlers) render(w http.ResponseWriter, r *http.Request, status int, form components.LoginForm) {
	if err := render.Render(r.Context(), w, status, components.LoginPage(form)); err != nil {
		h.logger.ErrorContext(r.Context(), "render login page", "error", err)
	}
}

// sanitizeNext ensures the post-login redirect target is a safe same-origin
// path, preventing open-redirect attacks. Ported from Nestova's auth
// handler: reject absolute, host-bearing, protocol-relative, and
// backslash-bearing targets (checking url.Parse's decoded Path so the
// percent-encoded form is caught too), then path.Clean, falling back to
// "/".
//
// The backslash check runs BEFORE path.Clean and rejects outright rather
// than stripping: path.Clean only treats '/' as a path separator, so a
// backslash survives it unchanged — but browsers normalize '\' to '/' when
// resolving a URL, so that string would still reach http.Redirect verbatim
// and then be followed as an off-origin, protocol-relative redirect.
func sanitizeNext(next string) string {
	if next == "" {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/") || strings.ContainsRune(u.Path, '\\') {
		return "/"
	}
	cleaned := path.Clean(u.Path)
	if !strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, "//") {
		return "/"
	}
	if u.RawQuery != "" {
		cleaned += "?" + u.RawQuery
	}
	return cleaned
}
