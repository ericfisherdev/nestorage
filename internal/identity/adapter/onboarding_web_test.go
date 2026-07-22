package adapter_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// fakeProvisioner is a configurable FirstAdminProvisioner fake: err (when
// set) is returned from CreateFirstAdmin, and the last created user is
// recorded under lock so assertions are race-free against the server
// goroutine that writes it.
type fakeProvisioner struct {
	err error

	mu    sync.Mutex
	calls int
	last  *domain.User
}

func (p *fakeProvisioner) CreateFirstAdmin(_ context.Context, u *domain.User) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.last = u
	if p.err != nil {
		return p.err
	}
	return nil
}

func (p *fakeProvisioner) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *fakeProvisioner) lastUser() *domain.User {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

type onboardingHarness struct {
	server      *httptest.Server
	client      *http.Client
	repo        *fakeUserExistence
	provisioner *fakeProvisioner
}

func newOnboardingHarness(t *testing.T, repo *fakeUserExistence, prov *fakeProvisioner) *onboardingHarness {
	t.Helper()
	sm := scs.New()
	handlers := adapter.NewOnboardingHandlers(repo, prov, sm, testLogger())

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
	return &onboardingHarness{server: server, client: client, repo: repo, provisioner: prov}
}

var csrfRe = regexp.MustCompile(`name="csrf_token"\s+value="([^"]*)"`)

// getCSRF performs the initial GET to obtain a session cookie (stored in the
// jar) and the embedded CSRF token.
func (h *onboardingHarness) getCSRF(t *testing.T) string {
	t.Helper()
	resp, err := h.client.Get(h.server.URL + "/setup")
	if err != nil {
		t.Fatalf("GET /setup: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	m := csrfRe.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no CSRF token in form:\n%s", body)
	}
	return string(m[1])
}

func (h *onboardingHarness) post(t *testing.T, form url.Values) (*http.Response, string) {
	t.Helper()
	resp, err := h.client.PostForm(h.server.URL+"/setup", form)
	if err != nil {
		t.Fatalf("POST /setup: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(body)
}

func validSubmission(csrf string) url.Values {
	return url.Values{
		"csrf_token":            {csrf},
		"display_name":          {"Maya"},
		"email":                 {"Maya@Example.com"},
		"password":              {"correct-horse-battery-staple"},
		"password_confirmation": {"correct-horse-battery-staple"},
	}
}

func TestPage_NoAdmin_RendersForm(t *testing.T) {
	h := newOnboardingHarness(t, &fakeUserExistence{hasAny: false}, &fakeProvisioner{})
	resp, err := h.client.Get(h.server.URL + "/setup")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), `action="/setup"`) {
		t.Fatalf("form action missing:\n%s", body)
	}
	if !csrfRe.Match(body) {
		t.Fatal("CSRF token field missing")
	}
}

func TestPage_AdminExists_RedirectsToRoot(t *testing.T) {
	h := newOnboardingHarness(t, &fakeUserExistence{hasAny: true}, &fakeProvisioner{})
	resp, err := h.client.Get(h.server.URL + "/setup")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Fatalf("Location = %q, want /", loc)
	}
	if h.provisioner.callCount() != 0 {
		t.Fatal("GET /setup with an admin present must never provision anything")
	}
}

func TestSubmit_MissingCSRF_Forbidden(t *testing.T) {
	h := newOnboardingHarness(t, &fakeUserExistence{hasAny: false}, &fakeProvisioner{})
	resp, _ := h.post(t, url.Values{"display_name": {"Maya"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if h.provisioner.callCount() != 0 {
		t.Fatal("provisioner called despite a missing CSRF token")
	}
}

func TestSubmit_WrongCSRF_Forbidden(t *testing.T) {
	h := newOnboardingHarness(t, &fakeUserExistence{hasAny: false}, &fakeProvisioner{})
	h.getCSRF(t)
	form := validSubmission("not-the-real-token")
	resp, _ := h.post(t, form)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestSubmit_AdminAlreadyExists_RedirectsToRoot(t *testing.T) {
	repo := &fakeUserExistence{hasAny: false}
	h := newOnboardingHarness(t, repo, &fakeProvisioner{})
	csrf := h.getCSRF(t)
	// Simulate the race: an admin was created between the GET and this POST.
	repo.hasAny = true

	resp, _ := h.post(t, validSubmission(csrf))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Fatalf("Location = %q, want /", loc)
	}
	if h.provisioner.callCount() != 0 {
		t.Fatal("provisioner must not be called once the re-check finds an admin")
	}
}

func TestSubmit_ValidationFailures(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(url.Values)
		wantMsg string
	}{
		{"empty display name", func(f url.Values) { f.Set("display_name", "") }, "name is required"},
		{"empty email", func(f url.Values) { f.Set("email", "") }, "valid email"},
		{"email missing @", func(f url.Values) { f.Set("email", "not-an-email") }, "valid email"},
		{"password mismatch", func(f url.Values) { f.Set("password_confirmation", "different") }, "do not match"},
		{"password too short", func(f url.Values) {
			f.Set("password", "short")
			f.Set("password_confirmation", "short")
		}, "at least 12"},
		{"password too long", func(f url.Values) {
			long := strings.Repeat("a", 129)
			f.Set("password", long)
			f.Set("password_confirmation", long)
		}, "at most 128"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newOnboardingHarness(t, &fakeUserExistence{hasAny: false}, &fakeProvisioner{})
			csrf := h.getCSRF(t)
			form := validSubmission(csrf)
			tt.mutate(form)

			resp, body := h.post(t, form)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422:\n%s", resp.StatusCode, body)
			}
			if !strings.Contains(body, tt.wantMsg) {
				t.Fatalf("body missing %q:\n%s", tt.wantMsg, body)
			}
			if strings.Contains(body, form.Get("password")) && form.Get("password") != "" {
				t.Fatal("password was echoed back into the form")
			}
			if h.provisioner.callCount() != 0 {
				t.Fatal("provisioner called despite a validation failure")
			}
		})
	}
}

func TestSubmit_ValidationFailure_PreservesNameAndEmail(t *testing.T) {
	h := newOnboardingHarness(t, &fakeUserExistence{hasAny: false}, &fakeProvisioner{})
	csrf := h.getCSRF(t)
	form := validSubmission(csrf)
	form.Set("password_confirmation", "does-not-match-at-all")

	_, body := h.post(t, form)
	if !strings.Contains(body, `value="Maya"`) {
		t.Errorf("display name not preserved on validation failure:\n%s", body)
	}
	if !strings.Contains(body, `value="maya@example.com"`) {
		t.Errorf("normalized email not preserved on validation failure:\n%s", body)
	}
}

func TestSubmit_Success_CreatesAdminAndSignsIn(t *testing.T) {
	prov := &fakeProvisioner{}
	h := newOnboardingHarness(t, &fakeUserExistence{hasAny: false}, prov)
	csrf := h.getCSRF(t)

	resp, _ := h.post(t, validSubmission(csrf))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Fatalf("Location = %q, want /", loc)
	}
	if prov.callCount() != 1 {
		t.Fatalf("provisioner called %d times, want 1", prov.callCount())
	}

	u := prov.lastUser()
	if u == nil {
		t.Fatal("no user was passed to the provisioner")
	}
	if u.DisplayName != "Maya" {
		t.Errorf("DisplayName = %q, want %q", u.DisplayName, "Maya")
	}
	if u.Email != "maya@example.com" {
		t.Errorf("Email = %q, want the normalized form %q", u.Email, "maya@example.com")
	}
	if u.Role != domain.RoleAdmin {
		t.Errorf("Role = %q, want %q", u.Role, domain.RoleAdmin)
	}
	if !strings.HasPrefix(u.PasswordHash, "$argon2id$") {
		t.Errorf("PasswordHash = %q, want a PHC-encoded argon2id hash", u.PasswordHash)
	}
	if u.PasswordHash == "correct-horse-battery-staple" {
		t.Error("PasswordHash stored the plaintext password")
	}

	// A session cookie must have been set (RenewToken + Put), so the new
	// admin is signed in without a separate login step.
	var found bool
	for _, c := range h.client.Jar.Cookies(mustParseURL(t, h.server.URL)) {
		if c.Name == "session" {
			found = true
		}
	}
	if !found {
		t.Error("no session cookie set after a successful setup")
	}
}

func TestSubmit_ProvisionerLostRace_RedirectsToRoot(t *testing.T) {
	prov := &fakeProvisioner{err: domain.ErrSetupComplete}
	h := newOnboardingHarness(t, &fakeUserExistence{hasAny: false}, prov)
	csrf := h.getCSRF(t)

	resp, _ := h.post(t, validSubmission(csrf))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Fatalf("Location = %q, want /", loc)
	}
}

func TestSubmit_ProvisionerError_500(t *testing.T) {
	prov := &fakeProvisioner{err: errors.New("boom")}
	h := newOnboardingHarness(t, &fakeUserExistence{hasAny: false}, prov)
	csrf := h.getCSRF(t)

	resp, _ := h.post(t, validSubmission(csrf))
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return u
}
