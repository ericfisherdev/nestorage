package adapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestcore/crypto"
	"github.com/ericfisherdev/nestcore/render"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
	"github.com/ericfisherdev/nestorage/web/components"
)

// existingUserChecker is the narrow read port (ISP) OnboardingHandlers
// depends on for the first-run guard: only HasAnyUser, satisfied by
// domain.UserRepository (a superset) and by test fakes.
type existingUserChecker interface {
	HasAnyUser(ctx context.Context) (bool, error)
}

// OnboardingHandlers serves the first-run admin onboarding wizard
// (GET/POST /setup) — the only route to creating the very first user.
type OnboardingHandlers struct {
	repo        existingUserChecker
	provisioner FirstAdminProvisioner
	sm          *scs.SessionManager
	logger      *slog.Logger
}

// NewOnboardingHandlers constructs OnboardingHandlers. All dependencies are
// required; a missing one panics at construction time (fail-fast, not at
// request time), matching every other WebHandlers constructor in this
// codebase.
func NewOnboardingHandlers(
	repo existingUserChecker,
	provisioner FirstAdminProvisioner,
	sm *scs.SessionManager,
	logger *slog.Logger,
) *OnboardingHandlers {
	if repo == nil {
		panic("identity/adapter: NewOnboardingHandlers requires a non-nil existingUserChecker")
	}
	if provisioner == nil {
		panic("identity/adapter: NewOnboardingHandlers requires a non-nil FirstAdminProvisioner")
	}
	if sm == nil {
		panic("identity/adapter: NewOnboardingHandlers requires a non-nil session manager")
	}
	if logger == nil {
		panic("identity/adapter: NewOnboardingHandlers requires a non-nil logger")
	}
	return &OnboardingHandlers{repo: repo, provisioner: provisioner, sm: sm, logger: logger}
}

// Routes registers the wizard's routes on mux.
func (h *OnboardingHandlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /setup", h.Page)
	mux.HandleFunc("POST /setup", h.Submit)
}

// Page handles GET /setup. If an admin already exists, setup is complete —
// redirect to / and never render the wizard, which is what "revisiting
// /setup afterwards does not create a user under any input" is measured
// against. Otherwise render the form with a fresh CSRF token.
func (h *OnboardingHandlers) Page(w http.ResponseWriter, r *http.Request) {
	has, err := h.repo.HasAnyUser(r.Context())
	if err != nil {
		h.logger.ErrorContext(r.Context(), "setup: check existing users", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if has {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	token := session.CSRFToken(r.Context(), h.sm)
	h.render(w, r, http.StatusOK, components.SetupForm{CSRFToken: token})
}

// Submit handles POST /setup: CSRF, re-check the first-run guard (closing
// the race between an earlier GET and this POST), validate, hash, create
// the first admin, sign them in, and redirect to /.
func (h *OnboardingHandlers) Submit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !session.VerifyCSRF(r, h.sm) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}

	has, err := h.repo.HasAnyUser(r.Context())
	if err != nil {
		h.logger.ErrorContext(r.Context(), "setup: re-check existing users", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if has {
		// Lost the first-run race between the GET and this POST: setup is
		// already complete.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	displayName := strings.TrimSpace(r.FormValue("display_name"))
	email := domain.NormalizeEmail(r.FormValue("email"))
	password := r.FormValue("password")
	confirmation := r.FormValue("password_confirmation")

	token := session.CSRFToken(r.Context(), h.sm)
	form := components.SetupForm{CSRFToken: token, DisplayName: displayName, Email: email}

	if validationErr := validateSetupForm(displayName, email, password, confirmation); validationErr != "" {
		form.Error = validationErr
		h.render(w, r, http.StatusUnprocessableEntity, form)
		return
	}

	hash, err := crypto.Hash(password)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "setup: hash password", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	u := &domain.User{
		ID:           domain.NewUserID(),
		DisplayName:  displayName,
		Email:        email,
		PasswordHash: hash,
		Role:         domain.RoleAdmin,
		Color:        domain.ColorIndigo,
	}
	if err := h.provisioner.CreateFirstAdmin(r.Context(), u); err != nil {
		if errors.Is(err, domain.ErrSetupComplete) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		h.logger.ErrorContext(r.Context(), "setup: create first admin", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Session-fixation defence: renew the token before storing any
	// privilege in the session.
	if err := h.sm.RenewToken(r.Context()); err != nil {
		h.logger.ErrorContext(r.Context(), "setup: renew session token", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h.sm.Put(r.Context(), session.KeyUserID, u.ID.String())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// render renders the setup page at the given status code.
func (h *OnboardingHandlers) render(w http.ResponseWriter, r *http.Request, status int, form components.SetupForm) {
	if err := render.Render(r.Context(), w, status, components.SetupPage(form)); err != nil {
		h.logger.ErrorContext(r.Context(), "render setup page", "error", err)
	}
}

// validateSetupForm returns a human-readable error message for the first
// validation failure found, or an empty string when the form is valid.
func validateSetupForm(displayName, email, password, confirmation string) string {
	switch {
	case displayName == "":
		return "Your name is required."
	case email == "" || !strings.Contains(email, "@"):
		return "Please enter a valid email address."
	case password != confirmation:
		return "Passwords do not match."
	default:
		switch err := domain.ValidatePassword(password); {
		case errors.Is(err, domain.ErrPasswordTooShort):
			return "Password must be at least 12 characters."
		case errors.Is(err, domain.ErrPasswordTooLong):
			return "Password must be at most 128 characters."
		default:
			return ""
		}
	}
}
