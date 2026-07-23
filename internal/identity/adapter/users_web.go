package adapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestcore/render"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
	"github.com/ericfisherdev/nestorage/web/components"
)

// adminService is the narrow port (ISP) UsersWebHandlers depends on,
// satisfied by *app.AdminService.
type adminService interface {
	List(ctx context.Context) ([]domain.User, error)
	Create(ctx context.Context, displayName, email, password string, role domain.Role, color domain.UserColor) (*domain.User, error)
	ChangeRole(ctx context.Context, id domain.UserID, role domain.Role) error
	Deactivate(ctx context.Context, id domain.UserID) error
	Reactivate(ctx context.Context, id domain.UserID) error
	ResetPassword(ctx context.Context, id domain.UserID, password string) error
}

// UsersWebHandlers serves the admin user-management screen (NSTR-21): the
// household's other users are listed, created, promoted/demoted,
// deactivated (never deleted — history keeps their name), reactivated, and
// password-reset here. Every route is registered on its own mux, mounted
// behind RequireAdmin by the composition root (cmd/server/main.go) — this
// type has no opinion about authorization, only about the admin operations
// themselves.
type UsersWebHandlers struct {
	admin  adminService
	sm     *scs.SessionManager
	layout requestLayoutFunc
	logger *slog.Logger
}

// NewUsersWebHandlers constructs UsersWebHandlers. All dependencies are
// required; a missing one panics at construction time, matching every other
// WebHandlers constructor in this codebase.
func NewUsersWebHandlers(admin adminService, sm *scs.SessionManager, layout requestLayoutFunc, logger *slog.Logger) *UsersWebHandlers {
	if admin == nil {
		panic("identity/adapter: NewUsersWebHandlers requires a non-nil adminService")
	}
	if sm == nil {
		panic("identity/adapter: NewUsersWebHandlers requires a non-nil session manager")
	}
	if layout == nil {
		panic("identity/adapter: NewUsersWebHandlers requires a non-nil layout func")
	}
	if logger == nil {
		panic("identity/adapter: NewUsersWebHandlers requires a non-nil logger")
	}
	return &UsersWebHandlers{admin: admin, sm: sm, layout: layout, logger: logger}
}

// Routes registers the admin user-management routes on mux. mux is expected
// to be mounted behind RequireAdmin by the caller — see this type's own doc.
func (h *UsersWebHandlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/users", h.List)
	mux.HandleFunc("POST /admin/users", h.Create)
	mux.HandleFunc("POST /admin/users/{id}/role", h.ChangeRole)
	mux.HandleFunc("POST /admin/users/{id}/deactivate", h.Deactivate)
	mux.HandleFunc("POST /admin/users/{id}/reactivate", h.Reactivate)
	mux.HandleFunc("POST /admin/users/{id}/password", h.ResetPassword)
}

// List handles GET /admin/users.
func (h *UsersWebHandlers) List(w http.ResponseWriter, r *http.Request) {
	h.renderList(w, r, http.StatusOK, "", "", "")
}

// Create handles POST /admin/users: CSRF, validate the add-user form, create
// via AdminService, and finish the way every mutation does.
func (h *UsersWebHandlers) Create(w http.ResponseWriter, r *http.Request) {
	if !h.verifyRequest(w, r) {
		return
	}

	displayName, email, password, role, color, msg := parseNewUserForm(r)
	if msg != "" {
		h.renderList(w, r, http.StatusUnprocessableEntity, msg, displayName, email)
		return
	}

	if _, err := h.admin.Create(r.Context(), displayName, email, password, role, color); err != nil {
		h.handleMutationError(w, r, err, displayName, email)
		return
	}
	h.finishMutation(w, r)
}

// ChangeRole handles POST /admin/users/{id}/role.
func (h *UsersWebHandlers) ChangeRole(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathUserID(w, r)
	if !ok {
		return
	}
	if !h.verifyRequest(w, r) {
		return
	}

	role, err := domain.ParseRole(r.FormValue("role"))
	if err != nil {
		h.renderList(w, r, http.StatusUnprocessableEntity, "Please choose a valid role.", "", "")
		return
	}
	if err := h.admin.ChangeRole(r.Context(), id, role); err != nil {
		h.handleMutationError(w, r, err, "", "")
		return
	}
	h.finishMutation(w, r)
}

// Deactivate handles POST /admin/users/{id}/deactivate.
func (h *UsersWebHandlers) Deactivate(w http.ResponseWriter, r *http.Request) {
	h.setActive(w, r, false)
}

// Reactivate handles POST /admin/users/{id}/reactivate.
func (h *UsersWebHandlers) Reactivate(w http.ResponseWriter, r *http.Request) {
	h.setActive(w, r, true)
}

// setActive backs both Deactivate and Reactivate: parse the path id, verify
// the request, call the matching AdminService method, and finish the same
// way every other mutation does.
func (h *UsersWebHandlers) setActive(w http.ResponseWriter, r *http.Request, active bool) {
	id, ok := h.pathUserID(w, r)
	if !ok {
		return
	}
	if !h.verifyRequest(w, r) {
		return
	}

	var err error
	if active {
		err = h.admin.Reactivate(r.Context(), id)
	} else {
		err = h.admin.Deactivate(r.Context(), id)
	}
	if err != nil {
		h.handleMutationError(w, r, err, "", "")
		return
	}
	h.finishMutation(w, r)
}

// ResetPassword handles POST /admin/users/{id}/password.
func (h *UsersWebHandlers) ResetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathUserID(w, r)
	if !ok {
		return
	}
	if !h.verifyRequest(w, r) {
		return
	}

	password := r.FormValue("password")
	if password != r.FormValue("password_confirmation") {
		h.renderList(w, r, http.StatusUnprocessableEntity, "Passwords do not match.", "", "")
		return
	}
	if err := h.admin.ResetPassword(r.Context(), id, password); err != nil {
		h.handleMutationError(w, r, err, "", "")
		return
	}
	h.finishMutation(w, r)
}

// pathUserID parses the {id} path value, answering 400 and reporting ok=false
// on a malformed one.
func (h *UsersWebHandlers) pathUserID(w http.ResponseWriter, r *http.Request) (domain.UserID, bool) {
	id, err := domain.ParseUserID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return domain.UserID{}, false
	}
	return id, true
}

// verifyRequest parses the form and verifies the CSRF token — the two checks
// every POST in this handler runs before doing anything else. Answers 400 or
// 403 and reports ok=false on failure.
func (h *UsersWebHandlers) verifyRequest(w http.ResponseWriter, r *http.Request) bool {
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

// finishMutation completes a successful mutation: an HTMX request gets the
// re-rendered list fragment, a full navigation gets redirected to
// /admin/users (so a page refresh does not resubmit the form).
func (h *UsersWebHandlers) finishMutation(w http.ResponseWriter, r *http.Request) {
	if render.IsHTMX(r) {
		h.renderList(w, r, http.StatusOK, "", "", "")
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// handleMutationError maps err to the list's inline error state when
// recognized (mapAdminError), or logs and answers a generic 500 otherwise.
func (h *UsersWebHandlers) handleMutationError(w http.ResponseWriter, r *http.Request, err error, newDisplayName, newEmail string) {
	if status, msg, ok := mapAdminError(err); ok {
		h.renderList(w, r, status, msg, newDisplayName, newEmail)
		return
	}
	h.logger.ErrorContext(r.Context(), "admin users: mutate", "error", err)
	http.Error(w, errInternalServerError, http.StatusInternalServerError)
}

// renderList loads the current user list and renders the screen at status,
// carrying formError and the add-user form's pending values — the shared
// tail of every handler above, success or failure alike.
func (h *UsersWebHandlers) renderList(w http.ResponseWriter, r *http.Request, status int, formError, newDisplayName, newEmail string) {
	users, err := h.admin.List(r.Context())
	if err != nil {
		h.logger.ErrorContext(r.Context(), "admin users: list", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	view := h.buildView(r.Context(), users, formError, newDisplayName, newEmail)
	h.renderPage(w, r, status, view)
}

// renderPage renders view at status: the bare table fragment for an
// HX-Request, or the full shell page for a normal navigation. This
// reimplements render.Page's own HX-Request split locally because
// render.Page hardcodes 200, and this handler needs to answer
// 409/422/404/500 on a mapped domain error while still re-rendering the
// screen's current state.
func (h *UsersWebHandlers) renderPage(w http.ResponseWriter, r *http.Request, status int, view components.UsersView) {
	w.Header().Set("Vary", "HX-Request")
	content := components.UsersTable(view)
	c := content
	if !render.IsHTMX(r) {
		c = h.layout(r, content)
	}
	if err := render.Render(r.Context(), w, status, c); err != nil {
		h.logger.ErrorContext(r.Context(), "admin users: render", "error", err)
	}
}

// buildView maps AdminService.List's result and this request's pending form
// state into the view-layer model UsersTable renders.
func (h *UsersWebHandlers) buildView(ctx context.Context, users []domain.User, formError, newDisplayName, newEmail string) components.UsersView {
	rows := make([]components.UserRowView, 0, len(users))
	for _, u := range users {
		rows = append(rows, components.UserRowView{
			ID:          u.ID.String(),
			DisplayName: u.DisplayName,
			Email:       u.Email,
			Role:        u.Role.String(),
			Active:      u.Active,
			Owner: components.OwnerView{
				Name:     u.DisplayName,
				Initials: initials(u.DisplayName),
				Color:    components.ParseOwnerColor(u.Color.String()),
			},
		})
	}
	return components.UsersView{
		CSRFToken:      session.CSRFToken(ctx, h.sm),
		Users:          rows,
		FormError:      formError,
		NewDisplayName: newDisplayName,
		NewEmail:       newEmail,
	}
}

// initials returns the first letter of name, uppercased, for OwnerAvatar's
// single-character badge — matching the one-letter initials the shell demo
// data already uses (cmd/server/shell.go's shellOwners). A rune slice is
// used so a multi-byte first character is not split.
func initials(name string) string {
	r := []rune(strings.TrimSpace(name))
	if len(r) == 0 {
		return "?"
	}
	return strings.ToUpper(string(r[0]))
}

// parseNewUserForm extracts and validates every add-user form field in one
// pass — Create has no reason to read r.FormValue a second time for fields
// this function already read. Returns the extracted displayName/email
// (always, even on failure, so the caller can pre-fill the re-rendered
// form) alongside password/role/color and an empty message on success, or a
// human-readable message naming the first validation failure. Password
// strength is NOT checked here — AdminService.Create delegates that to
// domain.ValidatePassword, so the rule stays defined in one place.
func parseNewUserForm(r *http.Request) (displayName, email, password string, role domain.Role, color domain.UserColor, message string) {
	displayName = strings.TrimSpace(r.FormValue("display_name"))
	email = domain.NormalizeEmail(r.FormValue("email"))
	password = r.FormValue("password")
	confirmation := r.FormValue("password_confirmation")

	switch {
	case displayName == "":
		return displayName, email, "", "", "", "Name is required."
	case email == "" || !strings.Contains(email, "@"):
		return displayName, email, "", "", "", "Please enter a valid email address."
	case password != confirmation:
		return displayName, email, "", "", "", "Passwords do not match."
	}

	role, err := domain.ParseRole(r.FormValue("role"))
	if err != nil {
		return displayName, email, "", "", "", "Please choose a valid role."
	}
	color, err = domain.ParseUserColor(r.FormValue("color"))
	if err != nil {
		return displayName, email, "", "", "", "Please choose a valid color."
	}
	return displayName, email, password, role, color, ""
}

// mapAdminError maps a domain error from an AdminService call to the HTTP
// status and inline message the list re-renders with. ok reports whether
// err was recognized; an unrecognized error is the caller's cue to log it
// and answer a generic 500 instead.
func mapAdminError(err error) (status int, message string, ok bool) {
	switch {
	case errors.Is(err, domain.ErrLastActiveAdmin):
		return http.StatusConflict, "This is the household's last active admin — promote someone else first.", true
	case errors.Is(err, domain.ErrUserNotFound):
		return http.StatusNotFound, "That user no longer exists.", true
	case errors.Is(err, domain.ErrDuplicateEmail):
		return http.StatusUnprocessableEntity, "That email is already in use.", true
	case errors.Is(err, domain.ErrPasswordTooShort):
		return http.StatusUnprocessableEntity, "Password must be at least 12 characters.", true
	case errors.Is(err, domain.ErrPasswordTooLong):
		return http.StatusUnprocessableEntity, "Password must be at most 128 characters.", true
	default:
		return 0, "", false
	}
}
