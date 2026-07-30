package session

import (
	"context"
	"testing"
	"time"

	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
)

// TestIdentityStore_NonCtxDelegatesMatchCtx covers Find/Commit/Delete — the
// non-context scs.Store methods, which scs.SessionManager falls back to
// only when a caller does not go through the Ctx variants (identity_store.go
// gated_test.go's own round-trip tests exercise those instead).
func TestIdentityStore_NonCtxDelegatesMatchCtx(t *testing.T) {
	pool := dbtest.Harness.NewIsolatedPool(t, "session")
	store := newIdentityStore(pool)
	defer store.StopCleanup()

	const token = "test-token-noctx"
	if err := store.Commit(token, []byte("payload"), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	data, found, err := store.Find(token)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if !found || string(data) != "payload" {
		t.Errorf("Find = (%q, %v), want (%q, true)", data, found, "payload")
	}

	if err := store.Delete(token); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found, err := store.Find(token); err != nil || found {
		t.Errorf("Find after Delete = (found=%v, err=%v), want found=false, err=nil", found, err)
	}
}

// TestIdentityStore_FindCtx_ExpiredReportsNotFound proves FindCtx's
// `expiry > now()` clause: an expired row is treated as absent, not as a
// hit whose caller must separately check expiry.
func TestIdentityStore_FindCtx_ExpiredReportsNotFound(t *testing.T) {
	pool := dbtest.Harness.NewIsolatedPool(t, "session")
	store := newIdentityStore(pool)
	defer store.StopCleanup()

	const token = "expired-token"
	if err := store.CommitCtx(t.Context(), token, []byte("payload"), time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("CommitCtx: %v", err)
	}
	_, found, err := store.FindCtx(t.Context(), token)
	if err != nil {
		t.Fatalf("FindCtx: %v", err)
	}
	if found {
		t.Error("FindCtx on an expired row = found true, want false")
	}
}

// TestIdentityStore_StartCleanup_PurgesExpiredRows proves the background
// cleanup goroutine actually deletes expired rows, not just that the query
// compiles.
func TestIdentityStore_StartCleanup_PurgesExpiredRows(t *testing.T) {
	pool := dbtest.Harness.NewIsolatedPool(t, "session")
	store := &identityStore{pool: pool}

	const token = "cleanup-token"
	if err := store.CommitCtx(t.Context(), token, []byte("x"), time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("CommitCtx: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		store.startCleanup(ctx, 20*time.Millisecond)
		close(done)
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM identity.sessions WHERE token = $1", token).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("row count after the cleanup ticker fired = %d, want 0 (expired row purged)", count)
	}
}

// TestIdentityStore_StartCleanup_NonPositiveIntervalIsNoOp proves a
// zero/negative interval returns immediately rather than starting a ticker
// that would panic (time.NewTicker rejects a non-positive duration).
func TestIdentityStore_StartCleanup_NonPositiveIntervalIsNoOp(t *testing.T) {
	store := &identityStore{}
	store.startCleanup(t.Context(), 0)
}
