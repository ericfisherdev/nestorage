package adapter_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
)

// fakeCurrentUserRepo is a configurable currentUserFinder fake for
// Authenticate's tests. A non-nil err simulates a transient repository
// failure; otherwise users maps id -> User, with a missing entry reporting
// domain.ErrUserNotFound like the real repository does.
type fakeCurrentUserRepo struct {
	users map[domain.UserID]*domain.User
	err   error
}

func (f *fakeCurrentUserRepo) FindByID(_ context.Context, id domain.UserID) (*domain.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	u, ok := f.users[id]
	if !ok {
		return nil, domain.ErrUserNotFound
	}
	return u, nil
}

// protectedHandler reports the CurrentUser resolved into the request
// context, so a test can assert both that RequireUser let the request
// through AND that Authenticate resolved the right user.
func protectedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := adapter.CurrentUser(r.Context()); ok {
			w.Header().Set("X-User-Id", u.ID.String())
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequireUser_Anonymous_FullNavigation_RedirectsToLogin(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /bins", adapter.RequireUser()(protectedHandler()))

	req := httptest.NewRequest(http.MethodGet, "/bins", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if loc := rec.Header().Get("Location"); loc != "/login?next=%2Fbins" {
		t.Errorf("Location = %q, want %q", loc, "/login?next=%2Fbins")
	}
}

func TestRequireUser_Anonymous_HTMXRequest_Returns401(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /bins", adapter.RequireUser()(protectedHandler()))

	req := httptest.NewRequest(http.MethodGet, "/bins", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// authenticatedServer wires a real (in-memory) session manager: Authenticate
// resolves the session's stored user into context, and RequireUser gates a
// protected resource behind it — the same chain cmd/server/main.go builds,
// minus Postgres. The /seed route stands in for Handlers.Login's own
// session establishment (tested separately in web_test.go), letting these
// tests drive Authenticate/RequireUser in isolation.
func authenticatedServer(t *testing.T, repo *fakeCurrentUserRepo) *httptest.Server {
	t.Helper()
	sm := scs.New()
	authenticate := adapter.Authenticate(sm, repo, testLogger())

	mux := http.NewServeMux()
	mux.Handle("GET /bins", authenticate(adapter.RequireUser()(protectedHandler())))
	mux.HandleFunc("POST /seed", func(w http.ResponseWriter, r *http.Request) {
		sm.Put(r.Context(), session.KeyUserID, r.FormValue("user_id"))
		w.WriteHeader(http.StatusNoContent)
	})

	server := httptest.NewServer(sm.LoadAndSave(mux))
	t.Cleanup(server.Close)
	return server
}

func newSeededClient(t *testing.T, server *httptest.Server, userID domain.UserID) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	client := &http.Client{
		Jar: jar,
		// /login is not registered on this test mux (Authenticate/
		// RequireUser are exercised in isolation from Handlers.Login here),
		// so following RequireUser's redirect would 404. Stop at the
		// redirect itself, which is what these tests assert on.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.PostForm(server.URL+"/seed", url.Values{"user_id": {userID.String()}})
	if err != nil {
		t.Fatalf("POST /seed: %v", err)
	}
	_ = resp.Body.Close()
	return client
}

func TestAuthenticate_ValidSession_PassesThrough(t *testing.T) {
	userID := domain.NewUserID()
	repo := &fakeCurrentUserRepo{users: map[domain.UserID]*domain.User{
		userID: {ID: userID, Active: true},
	}}
	server := authenticatedServer(t, repo)
	client := newSeededClient(t, server, userID)

	resp, err := client.Get(server.URL + "/bins")
	if err != nil {
		t.Fatalf("GET /bins: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an authenticated user must pass)", resp.StatusCode)
	}
	if got := resp.Header.Get("X-User-Id"); got != userID.String() {
		t.Errorf("X-User-Id = %q, want %q", got, userID.String())
	}
}

func TestAuthenticate_UnknownUser_ClearsSessionAndBlocks(t *testing.T) {
	repo := &fakeCurrentUserRepo{users: map[domain.UserID]*domain.User{}}
	server := authenticatedServer(t, repo)
	client := newSeededClient(t, server, domain.NewUserID())

	resp, err := client.Get(server.URL + "/bins")
	if err != nil {
		t.Fatalf("GET /bins: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (a session naming an unknown user must be treated as anonymous)", resp.StatusCode, http.StatusSeeOther)
	}
}

func TestAuthenticate_InactiveUser_ClearsSessionAndBlocks(t *testing.T) {
	userID := domain.NewUserID()
	repo := &fakeCurrentUserRepo{users: map[domain.UserID]*domain.User{
		userID: {ID: userID, Active: false},
	}}
	server := authenticatedServer(t, repo)
	client := newSeededClient(t, server, userID)

	resp, err := client.Get(server.URL + "/bins")
	if err != nil {
		t.Fatalf("GET /bins: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (a deactivated user must be treated as anonymous)", resp.StatusCode, http.StatusSeeOther)
	}
}

// TestAuthenticate_TransientRepositoryError_ProceedsAnonymouslyWithoutClearingSession
// asserts a repository outage does not sign the user out: the request
// proceeds anonymously for THIS request, but the session key is left
// intact, so a later request (once the repository recovers) authenticates
// again with no need to log in a second time.
func TestAuthenticate_TransientRepositoryError_ProceedsAnonymouslyWithoutClearingSession(t *testing.T) {
	userID := domain.NewUserID()
	repo := &fakeCurrentUserRepo{err: errors.New("boom")}
	server := authenticatedServer(t, repo)
	client := newSeededClient(t, server, userID)

	resp, err := client.Get(server.URL + "/bins")
	if err != nil {
		t.Fatalf("GET /bins: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status during the outage = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	// The repository recovers; the session key must still be present for
	// this SAME client (no re-login) to now authenticate successfully.
	repo.err = nil
	repo.users = map[domain.UserID]*domain.User{userID: {ID: userID, Active: true}}

	resp2, err := client.Get(server.URL + "/bins")
	if err != nil {
		t.Fatalf("GET /bins (after recovery): %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("status after the repository recovers = %d, want %d (the session key must not have been cleared by the earlier transient error)", resp2.StatusCode, http.StatusOK)
	}
}
