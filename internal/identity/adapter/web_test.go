package adapter_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// fakeAuthenticator is a configurable loginAuthenticator fake: users maps a
// normalized email to the password that must be presented and the UserID
// Login returns on a match. calls is tracked so tests can assert the
// attempt limiter short-circuits BEFORE the Authenticator is ever touched.
type fakeAuthenticator struct {
	users map[string]struct {
		password string
		userID   domain.UserID
	}

	mu    sync.Mutex
	calls int
}

func (f *fakeAuthenticator) Login(_ context.Context, email, password string) (domain.UserID, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	cred, ok := f.users[email]
	if !ok || cred.password != password {
		return domain.UserID{}, domain.ErrInvalidCredentials
	}
	return cred.userID, nil
}

func (f *fakeAuthenticator) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// newFakeAuthenticator seeds one valid email/password pair, returning the
// fake and the UserID a successful Login reports.
func newFakeAuthenticator(email, password string) (*fakeAuthenticator, domain.UserID) {
	userID := domain.NewUserID()
	return &fakeAuthenticator{
		users: map[string]struct {
			password string
			userID   domain.UserID
		}{
			email: {password: password, userID: userID},
		},
	}, userID
}

type loginHarness struct {
	server *httptest.Server
	client *http.Client
	authn  *fakeAuthenticator
}

func newLoginHarness(t *testing.T, authn *fakeAuthenticator) *loginHarness {
	t.Helper()
	sm := scs.New()
	handlers := adapter.NewHandlers(sm, authn, testLogger())

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
	return &loginHarness{server: server, client: client, authn: authn}
}

// getCSRF performs the initial GET to obtain a session cookie (stored in
// the jar) and the login form's embedded CSRF token.
func (h *loginHarness) getCSRF(t *testing.T) string {
	t.Helper()
	resp, err := h.client.Get(h.server.URL + "/login")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	m := csrfRe.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no CSRF token in form:\n%s", body)
	}
	return string(m[1])
}

func (h *loginHarness) postForm(t *testing.T, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	resp, err := h.client.PostForm(h.server.URL+path, form)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(body)
}

func loginForm(csrf, email, password, next string) url.Values {
	return url.Values{
		"csrf_token": {csrf},
		"email":      {email},
		"password":   {password},
		"next":       {next},
	}
}

func TestNewHandlers_NilDependenciesPanic(t *testing.T) {
	authn, _ := newFakeAuthenticator("alice@example.com", "correct-horse-battery")
	t.Run("nil session manager", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewHandlers(nil, authn, logger) did not panic")
			}
		}()
		adapter.NewHandlers(nil, authn, testLogger())
	})
	t.Run("nil authenticator", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewHandlers(sm, nil, logger) did not panic")
			}
		}()
		adapter.NewHandlers(scs.New(), nil, testLogger())
	})
	t.Run("nil logger", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewHandlers(sm, authn, nil) did not panic")
			}
		}()
		adapter.NewHandlers(scs.New(), authn, nil)
	})
}

func TestLoginPage_RendersFormWithCSRFToken(t *testing.T) {
	authn, _ := newFakeAuthenticator("alice@example.com", "correct-horse-battery")
	h := newLoginHarness(t, authn)

	resp, err := h.client.Get(h.server.URL + "/login")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), `action="/login"`) {
		t.Fatalf("form action missing:\n%s", body)
	}
	if !csrfRe.Match(body) {
		t.Fatal("CSRF token field missing")
	}
}

func TestLogin_MissingCSRF_Forbidden(t *testing.T) {
	authn, _ := newFakeAuthenticator("alice@example.com", "correct-horse-battery")
	h := newLoginHarness(t, authn)

	resp, _ := h.postForm(t, "/login", url.Values{"email": {"alice@example.com"}, "password": {"correct-horse-battery"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if authn.callCount() != 0 {
		t.Error("Authenticator called despite a missing CSRF token")
	}
}

func TestLogin_WrongCredentials_GenericMessage(t *testing.T) {
	authn, _ := newFakeAuthenticator("alice@example.com", "correct-horse-battery")
	h := newLoginHarness(t, authn)
	csrf := h.getCSRF(t)

	resp, body := h.postForm(t, "/login", loginForm(csrf, "alice@example.com", "wrong-password", ""))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Invalid email or password.") {
		t.Errorf("body missing the generic message:\n%s", body)
	}
}

func TestLogin_UnknownEmail_SameGenericMessageAsWrongPassword(t *testing.T) {
	authn, _ := newFakeAuthenticator("alice@example.com", "correct-horse-battery")
	h := newLoginHarness(t, authn)
	csrf := h.getCSRF(t)

	resp, body := h.postForm(t, "/login", loginForm(csrf, "nobody@example.com", "anything", ""))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Invalid email or password.") {
		t.Errorf("body missing the generic message:\n%s", body)
	}
}

func TestLogin_Success_RotatesSessionAndRedirectsToRoot(t *testing.T) {
	authn, wantID := newFakeAuthenticator("alice@example.com", "correct-horse-battery")
	h := newLoginHarness(t, authn)
	csrf := h.getCSRF(t)
	preToken := sessionCookieValue(h.client, h.server.URL)

	resp, body := h.postForm(t, "/login", loginForm(csrf, "alice@example.com", "correct-horse-battery", ""))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303:\n%s", resp.StatusCode, body)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want %q", loc, "/")
	}
	if postToken := sessionCookieValue(h.client, h.server.URL); postToken == "" || postToken == preToken {
		t.Error("successful login did not rotate the session token — a token captured pre-login must not remain valid")
	}
	_ = wantID
}

func TestLogin_Success_RedirectsToSanitizedNext(t *testing.T) {
	tests := []struct {
		name string
		next string
		want string
	}{
		{"same-origin path is preserved", "/bins", "/bins"},
		{"absolute URL is rejected", "https://evil.example/steal", "/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authn, _ := newFakeAuthenticator("alice@example.com", "correct-horse-battery")
			h := newLoginHarness(t, authn)
			csrf := h.getCSRF(t)

			resp, _ := h.postForm(t, "/login", loginForm(csrf, "alice@example.com", "correct-horse-battery", tt.next))
			if loc := resp.Header.Get("Location"); loc != tt.want {
				t.Errorf("Location = %q, want %q", loc, tt.want)
			}
		})
	}
}

// TestLogin_LockedOut_RejectsEvenCorrectCredentialsWithoutTouchingAuthenticator
// drives the limiter past its threshold, then asserts a subsequent attempt
// with the CORRECT password still fails — and that the Authenticator was
// never even called for that locked-out attempt, matching the "check the
// limiter before touching the Authenticator" design.
func TestLogin_LockedOut_RejectsEvenCorrectCredentialsWithoutTouchingAuthenticator(t *testing.T) {
	const (
		email    = "alice@example.com"
		password = "correct-horse-battery"
	)
	authn, _ := newFakeAuthenticator(email, password)
	h := newLoginHarness(t, authn)

	// loginAttemptThreshold+1 wrong attempts crosses into the lockout.
	for range 6 {
		csrf := h.getCSRF(t)
		h.postForm(t, "/login", loginForm(csrf, email, "wrong-password", ""))
	}
	callsBeforeLockedAttempt := authn.callCount()

	csrf := h.getCSRF(t)
	resp, body := h.postForm(t, "/login", loginForm(csrf, email, password, ""))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("locked-out attempt with correct credentials: status = %d, want 401:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Invalid email or password.") {
		t.Errorf("locked-out body missing the generic message:\n%s", body)
	}
	if authn.callCount() != callsBeforeLockedAttempt {
		t.Error("Authenticator was called for a locked-out attempt — the limiter must short-circuit before it")
	}
}

func TestLogout_MissingCSRF_Forbidden(t *testing.T) {
	authn, _ := newFakeAuthenticator("alice@example.com", "correct-horse-battery")
	h := newLoginHarness(t, authn)

	resp, _ := h.postForm(t, "/logout", url.Values{})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestLogout_DestroysSessionAndRedirectsToLogin(t *testing.T) {
	authn, _ := newFakeAuthenticator("alice@example.com", "correct-horse-battery")
	h := newLoginHarness(t, authn)
	csrf := h.getCSRF(t)
	h.postForm(t, "/login", loginForm(csrf, "alice@example.com", "correct-horse-battery", ""))
	postLoginToken := sessionCookieValue(h.client, h.server.URL)

	// Logout needs its own CSRF token (a fresh GET carries the current
	// session's token, same as the login form does).
	logoutCSRF := h.getCSRF(t)
	resp, _ := h.postForm(t, "/logout", url.Values{"csrf_token": {logoutCSRF}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want %q", loc, "/login")
	}
	if sessionCookieValue(h.client, h.server.URL) == postLoginToken {
		t.Error("logout did not change the session token — the server-side session must be destroyed, not just the cookie")
	}
}
