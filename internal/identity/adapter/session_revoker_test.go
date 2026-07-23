package adapter_test

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	corecfg "github.com/ericfisherdev/nestcore/config"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
)

// sessionRevokerFixture wires a real pgxstore-backed session manager over
// one derived database, plus a tiny HTTP server that lets a test establish
// a real, committed session row for an arbitrary user id. This cannot be
// replaced with the in-memory scs.New() store the rest of this package's
// tests use: SessionRevoker.RevokeAll depends on sm.Iterate, which needs
// the store to implement scs.IterableStore (see SessionRevoker's own doc).
type sessionRevokerFixture struct {
	pool   *pgxpool.Pool
	sm     *scs.SessionManager
	server *httptest.Server
}

func newSessionRevokerFixture(t *testing.T) *sessionRevokerFixture {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "identity")
	sm := session.New(pool, corecfg.SessionConfig{Lifetime: time.Hour})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /seed", func(w http.ResponseWriter, r *http.Request) {
		sm.Put(r.Context(), session.KeyUserID, r.FormValue("user_id"))
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(sm.LoadAndSave(mux))
	t.Cleanup(server.Close)

	return &sessionRevokerFixture{pool: pool, sm: sm, server: server}
}

// seedSession establishes a real, committed session row naming userID.
func (f *sessionRevokerFixture) seedSession(t *testing.T, userID domain.UserID) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	client := &http.Client{Jar: jar}
	resp, err := client.PostForm(f.server.URL+"/seed", url.Values{"user_id": {userID.String()}})
	if err != nil {
		t.Fatalf("POST /seed: %v", err)
	}
	_ = resp.Body.Close()
}

// sessionRowCount queries the sessions table pgxstore owns directly, so
// these tests assert on the real server-side rows, mirroring
// login_gated_test.go's own helper of the same name.
func (f *sessionRevokerFixture) sessionRowCount(ctx context.Context, t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM sessions").Scan(&n); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	return n
}

// TestSessionRevoker_RevokeAll_DestroysOnlyTargetUsersSessions is the
// automated equivalent of this ticket's "deactivating a user immediately
// invalidates their sessions" criterion, at the SessionRevoker layer: two
// real session rows, only the target's is destroyed.
func TestSessionRevoker_RevokeAll_DestroysOnlyTargetUsersSessions(t *testing.T) {
	f := newSessionRevokerFixture(t)
	ctx := testCtx(t)

	targetID := domain.NewUserID()
	otherID := domain.NewUserID()
	f.seedSession(t, targetID)
	f.seedSession(t, otherID)
	if got := f.sessionRowCount(ctx, t); got != 2 {
		t.Fatalf("session rows after seeding two = %d, want 2", got)
	}

	revoker := adapter.NewSessionRevoker(f.sm)
	if err := revoker.RevokeAll(ctx, targetID); err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}

	if got := f.sessionRowCount(ctx, t); got != 1 {
		t.Errorf("session rows after RevokeAll(target) = %d, want 1 (only the other user's session survives)", got)
	}
}

// TestSessionRevoker_RevokeAll_NothingToRevokeIsNotAnError asserts that a
// user with no active sessions at all is not an error case — CredentialRevoker's
// own contract (see app.CredentialRevoker's doc).
func TestSessionRevoker_RevokeAll_NothingToRevokeIsNotAnError(t *testing.T) {
	f := newSessionRevokerFixture(t)
	revoker := adapter.NewSessionRevoker(f.sm)

	if err := revoker.RevokeAll(testCtx(t), domain.NewUserID()); err != nil {
		t.Errorf("RevokeAll with no sessions at all = %v, want nil", err)
	}
}

func TestNewSessionRevoker_NilSessionManagerPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewSessionRevoker(nil) did not panic")
		}
	}()
	adapter.NewSessionRevoker(nil)
}
