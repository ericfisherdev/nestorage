package adapter_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	corecfg "github.com/ericfisherdev/nestcore/config"
	"github.com/ericfisherdev/nestcore/crypto/cryptotest"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/app"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
)

// loginGatedFixture wires the real UserRepository, Authenticator, and a
// pgxstore-backed session manager over one derived database — the same
// composition cmd/server/main.go builds, minus the rest of the app's
// routes. Hashing uses cryptotest.Hasher()'s cheap parameters so the suite
// does not pay a 64 MiB argon2 derivation per login.
type loginGatedFixture struct {
	pool   *pgxpool.Pool
	server *httptest.Server
	client *http.Client
}

func newLoginGatedFixture(t *testing.T, email, password string) *loginGatedFixture {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "identity")
	repo := adapter.NewUserRepository(pool)

	hash, err := cryptotest.Hasher().Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	u := &domain.User{
		ID:           domain.NewUserID(),
		DisplayName:  "Alice",
		Email:        email,
		PasswordHash: hash,
		Role:         domain.RoleAdmin,
		Color:        domain.ColorIndigo,
	}
	if err := repo.Create(testCtx(t), u); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	authn := app.NewAuthenticator(repo, cryptotest.Hasher())
	sm := session.New(pool, corecfg.SessionConfig{Lifetime: time.Hour})
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
	return &loginGatedFixture{pool: pool, server: server, client: client}
}

// sessionRowCount queries the sessions table pgxstore owns directly, so
// these tests assert on the real server-side row — not just on cookie
// behavior, which the hermetic (in-memory store) tests already cover.
func (f *loginGatedFixture) sessionRowCount(ctx context.Context, t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM sessions").Scan(&n); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	return n
}

// TestLogin_WritesSessionRowAndRotatesToken is the automated equivalent of
// this ticket's fixation-defense acceptance criterion, against a real
// Postgres-backed session store: a successful login must write a sessions
// row, and the cookie token presented afterward must differ from the one
// issued before authentication.
func TestLogin_WritesSessionRowAndRotatesToken(t *testing.T) {
	const (
		email    = "alice@example.com"
		password = "correct-horse-battery-staple"
	)
	f := newLoginGatedFixture(t, email, password)
	ctx := testCtx(t)

	csrf := getLoginCSRF(t, f.client, f.server.URL)
	preToken := sessionCookieValue(f.client, f.server.URL)

	resp, err := f.client.PostForm(f.server.URL+"/login", loginForm(csrf, email, password, ""))
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}

	postToken := sessionCookieValue(f.client, f.server.URL)
	if postToken == "" || postToken == preToken {
		t.Error("successful login did not rotate the session token")
	}
	if n := f.sessionRowCount(ctx, t); n < 1 {
		t.Errorf("sessions row count after login = %d, want at least 1", n)
	}
}

// TestLogout_DeletesSessionRow is the automated equivalent of "logout
// invalidates the session server-side, not only the cookie": after logout,
// the sessions table must have one fewer row than it did right after
// login.
func TestLogout_DeletesSessionRow(t *testing.T) {
	const (
		email    = "bob@example.com"
		password = "correct-horse-battery-staple"
	)
	f := newLoginGatedFixture(t, email, password)
	ctx := testCtx(t)

	csrf := getLoginCSRF(t, f.client, f.server.URL)
	loginResp, err := f.client.PostForm(f.server.URL+"/login", loginForm(csrf, email, password, ""))
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	_ = loginResp.Body.Close()
	rowsAfterLogin := f.sessionRowCount(ctx, t)

	logoutCSRF := getLoginCSRF(t, f.client, f.server.URL)
	logoutResp, err := f.client.PostForm(f.server.URL+"/logout", loginForm(logoutCSRF, "", "", ""))
	if err != nil {
		t.Fatalf("POST /logout: %v", err)
	}
	_ = logoutResp.Body.Close()
	if logoutResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout status = %d, want 303", logoutResp.StatusCode)
	}

	if rowsAfterLogout := f.sessionRowCount(ctx, t); rowsAfterLogout >= rowsAfterLogin {
		t.Errorf("sessions row count after logout = %d, want fewer than %d (post-login)", rowsAfterLogout, rowsAfterLogin)
	}
}

// getLoginCSRF performs a GET against the login page and returns the
// embedded CSRF token, failing the test if the form (or its token) is
// missing.
func getLoginCSRF(t *testing.T, client *http.Client, serverURL string) string {
	t.Helper()
	resp, err := client.Get(serverURL + "/login")
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
