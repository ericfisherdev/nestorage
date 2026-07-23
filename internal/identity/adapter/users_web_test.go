package adapter_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// fakeAdminService is a configurable adminService fake for
// UsersWebHandlers' hermetic unit tests. Each *Err field lets a test force
// the matching method to fail with a specific error, e.g. domain.ErrLastActiveAdmin.
type fakeAdminService struct {
	users []domain.User

	createErr        error
	changeRoleErr    error
	deactivateErr    error
	reactivateErr    error
	resetPasswordErr error

	createCalls int
}

func (f *fakeAdminService) List(_ context.Context) ([]domain.User, error) {
	return f.users, nil
}

func (f *fakeAdminService) Create(_ context.Context, displayName, email, _ string, role domain.Role, color domain.UserColor) (*domain.User, error) {
	f.createCalls++
	if f.createErr != nil {
		return nil, f.createErr
	}
	u := domain.User{ID: domain.NewUserID(), DisplayName: displayName, Email: email, Role: role, Color: color, Active: true}
	f.users = append(f.users, u)
	return &u, nil
}

func (f *fakeAdminService) ChangeRole(_ context.Context, _ domain.UserID, _ domain.Role) error {
	return f.changeRoleErr
}

func (f *fakeAdminService) Deactivate(_ context.Context, _ domain.UserID) error {
	return f.deactivateErr
}

func (f *fakeAdminService) Reactivate(_ context.Context, _ domain.UserID) error {
	return f.reactivateErr
}

func (f *fakeAdminService) ResetPassword(_ context.Context, _ domain.UserID, _ string) error {
	return f.resetPasswordErr
}

// testUsersLayout wraps content in an identifiable marker so tests can
// assert a full navigation was wrapped by it and an HTMX request was not,
// without depending on cmd/server's real shell layout.
func testUsersLayout(content templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if _, err := io.WriteString(w, "<layout>"); err != nil {
			return err
		}
		if err := content.Render(ctx, w); err != nil {
			return err
		}
		_, err := io.WriteString(w, "</layout>")
		return err
	})
}

type usersWebHarness struct {
	server *httptest.Server
	client *http.Client
	admin  *fakeAdminService
}

func newUsersWebHarness(t *testing.T, admin *fakeAdminService) *usersWebHarness {
	t.Helper()
	sm := scs.New()
	handlers := adapter.NewUsersWebHandlers(admin, sm, testUsersLayout, testLogger())

	mux := http.NewServeMux()
	handlers.Routes(mux)
	server := httptest.NewServer(sm.LoadAndSave(mux))
	t.Cleanup(server.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &usersWebHarness{server: server, client: client, admin: admin}
}

// getCSRF performs a GET against /admin/users and returns the embedded CSRF
// token, failing the test if the form (or its token) is missing.
func (h *usersWebHarness) getCSRF(t *testing.T) string {
	t.Helper()
	resp, err := h.client.Get(h.server.URL + "/admin/users")
	if err != nil {
		t.Fatalf("GET /admin/users: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	m := csrfRe.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no CSRF token in form:\n%s", body)
	}
	return string(m[1])
}

func (h *usersWebHarness) postForm(t *testing.T, path string, form url.Values, htmx bool) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.server.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(body)
}

func newUserForm(csrf, displayName, email, password, role, color string) url.Values {
	return url.Values{
		"csrf_token":            {csrf},
		"display_name":          {displayName},
		"email":                 {email},
		"password":              {password},
		"password_confirmation": {password},
		"role":                  {role},
		"color":                 {color},
	}
}

func TestNewUsersWebHandlers_NilDependenciesPanic(t *testing.T) {
	admin := &fakeAdminService{}
	sm := scs.New()
	tests := []struct {
		name string
		fn   func()
	}{
		{"nil admin service", func() { adapter.NewUsersWebHandlers(nil, sm, testUsersLayout, testLogger()) }},
		{"nil session manager", func() { adapter.NewUsersWebHandlers(admin, nil, testUsersLayout, testLogger()) }},
		{"nil layout", func() { adapter.NewUsersWebHandlers(admin, sm, nil, testLogger()) }},
		{"nil logger", func() { adapter.NewUsersWebHandlers(admin, sm, testUsersLayout, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewUsersWebHandlers(%s) did not panic", tt.name)
				}
			}()
			tt.fn()
		})
	}
}

func TestUsersWebHandlers_List_FullNavigation_WrapsInLayout(t *testing.T) {
	admin := &fakeAdminService{users: []domain.User{{ID: domain.NewUserID(), DisplayName: "Maya", Email: "maya@example.com", Role: domain.RoleMember, Active: true}}}
	h := newUsersWebHarness(t, admin)

	resp, err := h.client.Get(h.server.URL + "/admin/users")
	if err != nil {
		t.Fatalf("GET /admin/users: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "<layout>") {
		t.Error("full navigation response was not wrapped in the layout")
	}
	if !strings.Contains(string(body), "Maya") {
		t.Error("response missing the seeded user's display name")
	}
	if !csrfRe.Match(body) {
		t.Error("response missing the CSRF token field")
	}
}

func TestUsersWebHandlers_List_HTMXRequest_NoLayout(t *testing.T) {
	admin := &fakeAdminService{}
	h := newUsersWebHarness(t, admin)

	req, err := http.NewRequest(http.MethodGet, h.server.URL+"/admin/users", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("HX-Request", "true")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET /admin/users (HTMX): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if strings.Contains(string(body), "<layout>") {
		t.Error("an HTMX request must get the bare fragment, not the layout-wrapped page")
	}
}

func TestUsersWebHandlers_Create_MissingCSRF_Forbidden(t *testing.T) {
	admin := &fakeAdminService{}
	h := newUsersWebHarness(t, admin)

	resp, _ := h.postForm(t, "/admin/users", newUserForm("", "Maya", "maya@example.com", "correct-horse-battery-staple", "member", "indigo"), false)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if admin.createCalls != 0 {
		t.Error("AdminService.Create called despite a missing CSRF token")
	}
}

func TestUsersWebHandlers_Create_PasswordMismatch_UnprocessableEntity(t *testing.T) {
	admin := &fakeAdminService{}
	h := newUsersWebHarness(t, admin)
	csrf := h.getCSRF(t)

	form := newUserForm(csrf, "Maya", "maya@example.com", "correct-horse-battery-staple", "member", "indigo")
	form.Set("password_confirmation", "does-not-match")
	resp, body := h.postForm(t, "/admin/users", form, false)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "do not match") {
		t.Errorf("body missing the mismatch message:\n%s", body)
	}
	if admin.createCalls != 0 {
		t.Error("AdminService.Create called despite a client-side validation failure")
	}
}

func TestUsersWebHandlers_Create_Success_FullNavigation_Redirects(t *testing.T) {
	admin := &fakeAdminService{}
	h := newUsersWebHarness(t, admin)
	csrf := h.getCSRF(t)

	resp, body := h.postForm(t, "/admin/users", newUserForm(csrf, "Maya", "maya@example.com", "correct-horse-battery-staple", "member", "indigo"), false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303:\n%s", resp.StatusCode, body)
	}
	if loc := resp.Header.Get("Location"); loc != "/admin/users" {
		t.Errorf("Location = %q, want %q", loc, "/admin/users")
	}
	if admin.createCalls != 1 {
		t.Errorf("AdminService.Create called %d times, want 1", admin.createCalls)
	}
}

func TestUsersWebHandlers_Create_Success_HTMX_RerendersFragment(t *testing.T) {
	admin := &fakeAdminService{}
	h := newUsersWebHarness(t, admin)
	csrf := h.getCSRF(t)

	resp, body := h.postForm(t, "/admin/users", newUserForm(csrf, "Maya", "maya@example.com", "correct-horse-battery-staple", "member", "indigo"), true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if strings.Contains(body, "<layout>") {
		t.Error("an HTMX response must be the bare fragment, not layout-wrapped")
	}
	if !strings.Contains(body, "Maya") {
		t.Errorf("re-rendered fragment missing the newly created user:\n%s", body)
	}
}

func TestUsersWebHandlers_ChangeRole_LastActiveAdmin_MapsTo409(t *testing.T) {
	admin := &fakeAdminService{changeRoleErr: domain.ErrLastActiveAdmin}
	h := newUsersWebHarness(t, admin)
	csrf := h.getCSRF(t)

	path := "/admin/users/" + domain.NewUserID().String() + "/role"
	resp, body := h.postForm(t, path, url.Values{"csrf_token": {csrf}, "role": {"member"}}, false)

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "last active admin") {
		t.Errorf("body missing the inline last-active-admin message:\n%s", body)
	}
}

func TestUsersWebHandlers_Deactivate_Success_FullNavigation_Redirects(t *testing.T) {
	admin := &fakeAdminService{}
	h := newUsersWebHarness(t, admin)
	csrf := h.getCSRF(t)

	path := "/admin/users/" + domain.NewUserID().String() + "/deactivate"
	resp, body := h.postForm(t, path, url.Values{"csrf_token": {csrf}}, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303:\n%s", resp.StatusCode, body)
	}
}

func TestUsersWebHandlers_ResetPassword_UserNotFound_MapsTo404(t *testing.T) {
	admin := &fakeAdminService{resetPasswordErr: domain.ErrUserNotFound}
	h := newUsersWebHarness(t, admin)
	csrf := h.getCSRF(t)

	path := "/admin/users/" + domain.NewUserID().String() + "/password"
	resp, body := h.postForm(t, path, url.Values{
		"csrf_token":            {csrf},
		"password":              {"correct-horse-battery-staple"},
		"password_confirmation": {"correct-horse-battery-staple"},
	}, false)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404:\n%s", resp.StatusCode, body)
	}
}

func TestUsersWebHandlers_ResetPassword_Mismatch_UnprocessableEntity(t *testing.T) {
	admin := &fakeAdminService{}
	h := newUsersWebHarness(t, admin)
	csrf := h.getCSRF(t)

	path := "/admin/users/" + domain.NewUserID().String() + "/password"
	resp, body := h.postForm(t, path, url.Values{
		"csrf_token":            {csrf},
		"password":              {"correct-horse-battery-staple"},
		"password_confirmation": {"does-not-match"},
	}, false)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "do not match") {
		t.Errorf("body missing the mismatch message:\n%s", body)
	}
}

func TestUsersWebHandlers_Reactivate_Success_FullNavigation_Redirects(t *testing.T) {
	admin := &fakeAdminService{}
	h := newUsersWebHarness(t, admin)
	csrf := h.getCSRF(t)

	path := "/admin/users/" + domain.NewUserID().String() + "/reactivate"
	resp, body := h.postForm(t, path, url.Values{"csrf_token": {csrf}}, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303:\n%s", resp.StatusCode, body)
	}
}

func TestUsersWebHandlers_ChangeRole_InvalidRole_UnprocessableEntity(t *testing.T) {
	admin := &fakeAdminService{}
	h := newUsersWebHarness(t, admin)
	csrf := h.getCSRF(t)

	path := "/admin/users/" + domain.NewUserID().String() + "/role"
	resp, body := h.postForm(t, path, url.Values{"csrf_token": {csrf}, "role": {"superuser"}}, false)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "valid role") {
		t.Errorf("body missing the invalid-role message:\n%s", body)
	}
}

func TestUsersWebHandlers_ChangeRole_MalformedID_BadRequest(t *testing.T) {
	admin := &fakeAdminService{}
	h := newUsersWebHarness(t, admin)

	resp, body := h.postForm(t, "/admin/users/not-a-uuid/role", url.Values{"role": {"admin"}}, false)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400:\n%s", resp.StatusCode, body)
	}
}

func TestUsersWebHandlers_Create_DuplicateEmail_MapsTo422(t *testing.T) {
	admin := &fakeAdminService{createErr: domain.ErrDuplicateEmail}
	h := newUsersWebHarness(t, admin)
	csrf := h.getCSRF(t)

	resp, body := h.postForm(t, "/admin/users", newUserForm(csrf, "Maya", "maya@example.com", "correct-horse-battery-staple", "member", "indigo"), false)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "already in use") {
		t.Errorf("body missing the duplicate-email message:\n%s", body)
	}
}

func TestUsersWebHandlers_Create_PasswordTooShort_MapsTo422(t *testing.T) {
	admin := &fakeAdminService{createErr: domain.ErrPasswordTooShort}
	h := newUsersWebHarness(t, admin)
	csrf := h.getCSRF(t)

	resp, body := h.postForm(t, "/admin/users", newUserForm(csrf, "Maya", "maya@example.com", "short-but-matching", "member", "indigo"), false)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "at least 12 characters") {
		t.Errorf("body missing the too-short message:\n%s", body)
	}
}

// TestUsersWebHandlers_Create_UnrecognizedError_MapsTo500 asserts an error
// mapAdminError does not recognize is logged and answered generically,
// rather than leaking internal detail to the response body.
func TestUsersWebHandlers_Create_UnrecognizedError_MapsTo500(t *testing.T) {
	admin := &fakeAdminService{createErr: errors.New("unexpected database explosion")}
	h := newUsersWebHarness(t, admin)
	csrf := h.getCSRF(t)

	resp, body := h.postForm(t, "/admin/users", newUserForm(csrf, "Maya", "maya@example.com", "correct-horse-battery-staple", "member", "indigo"), false)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500:\n%s", resp.StatusCode, body)
	}
	if strings.Contains(body, "database explosion") {
		t.Error("the unrecognized error's detail must not leak into the response body")
	}
}
