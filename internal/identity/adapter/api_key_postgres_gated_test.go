package adapter_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
)

// apiKeyFixture wires an APIKeyRepository over ONE derived database
// (dbtest.Harness.NewIsolatedPool must be called exactly once per test — see
// deviceTokenFixture's own doc for why a second call would wipe data already
// written). No gated test in this file uses t.Parallel(), for the same
// reason: every call shares the "identity" suffix, and NewIsolatedPool
// resets that database's schema on every call.
type apiKeyFixture struct {
	pool *pgxpool.Pool
	repo *adapter.APIKeyRepository
}

// newAPIKeyFixture derives this package's shared "identity" database — the
// same suffix every other gated test in this package uses (converging with
// NSTR-22's device token tests per NSTR-23's cross-ticket reconciliation),
// not an "identity_api_key" suffix of its own.
func newAPIKeyFixture(t *testing.T) *apiKeyFixture {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "identity")
	return &apiKeyFixture{pool: pool, repo: adapter.NewAPIKeyRepository(pool)}
}

func newAPIKeyForCreate(label string) *domain.APIKey {
	raw, err := domain.GenerateAPIKeySecret()
	if err != nil {
		panic(err)
	}
	return &domain.APIKey{
		ID:         domain.NewAPIKeyID(),
		KeyPrefix:  domain.KeyPrefixOf(raw),
		SecretHash: domain.HashAPIKeySecret(raw),
		Label:      label,
	}
}

func TestAPIKeyRepository_CreateAndGetBySecretHash(t *testing.T) {
	f := newAPIKeyFixture(t)
	key := newAPIKeyForCreate("Nestova integration")

	if err := f.repo.Create(testCtx(t), key); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if key.CreatedAt.IsZero() {
		t.Error("Create left CreatedAt zero")
	}

	got, err := f.repo.GetBySecretHash(testCtx(t), key.SecretHash)
	if err != nil {
		t.Fatalf("GetBySecretHash: %v", err)
	}
	if got.ID != key.ID || got.Label != "Nestova integration" || got.KeyPrefix != key.KeyPrefix {
		t.Errorf("GetBySecretHash = %+v, want it to match the created key", got)
	}
	if got.RevokedAt != nil || got.ExpiresAt != nil {
		t.Error("a freshly created key must have neither RevokedAt nor ExpiresAt set")
	}
}

// TestAPIKeyRepository_Create_SecondCurrentKeyRejected is the automated
// equivalent of this ticket's "at most one current key can exist, enforced
// by the database" acceptance criterion: the api_key_current_uniq partial
// unique index, not merely application code, rejects a second unsuperseded
// row.
func TestAPIKeyRepository_Create_SecondCurrentKeyRejected(t *testing.T) {
	f := newAPIKeyFixture(t)
	first := newAPIKeyForCreate("first")
	if err := f.repo.Create(testCtx(t), first); err != nil {
		t.Fatalf("Create(first): %v", err)
	}

	second := newAPIKeyForCreate("second")
	err := f.repo.Create(testCtx(t), second)
	if !errors.Is(err, domain.ErrAPIKeyExists) {
		t.Fatalf("Create(second current key) = %v, want ErrAPIKeyExists", err)
	}
}

func TestAPIKeyRepository_GetBySecretHash_UnknownHash(t *testing.T) {
	f := newAPIKeyFixture(t)

	_, err := f.repo.GetBySecretHash(testCtx(t), "nonexistent-hash")
	if !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Fatalf("GetBySecretHash(unknown) = %v, want ErrAPIKeyNotFound", err)
	}
}

func TestAPIKeyRepository_ListAll_NewestFirst(t *testing.T) {
	f := newAPIKeyFixture(t)
	first := newAPIKeyForCreate("first")
	if err := f.repo.Create(testCtx(t), first); err != nil {
		t.Fatalf("Create(first): %v", err)
	}
	// Rotate immediately, invalidating "first", so the second row is legal
	// under api_key_current_uniq and ListAll has two rows to order.
	second := newAPIKeyForCreate("second")
	if err := f.repo.Rotate(testCtx(t), first.ID, time.Now(), second); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	got, err := f.repo.ListAll(testCtx(t))
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListAll returned %d keys, want 2", len(got))
	}
	if got[0].ID != second.ID || got[1].ID != first.ID {
		t.Error("ListAll did not return newest first")
	}
}

func TestAPIKeyRepository_ListAll_EmptyIsNotAnError(t *testing.T) {
	f := newAPIKeyFixture(t)

	got, err := f.repo.ListAll(testCtx(t))
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListAll(no keys) returned %d, want 0", len(got))
	}
}

// TestAPIKeyRepository_Rotate_SupersedesIncumbentAndInsertsNext is the
// automated equivalent of this ticket's rotation acceptance criterion:
// rotating keeps the previous key working until the end of the chosen
// overlap window.
func TestAPIKeyRepository_Rotate_SupersedesIncumbentAndInsertsNext(t *testing.T) {
	f := newAPIKeyFixture(t)
	incumbent := newAPIKeyForCreate("incumbent")
	if err := f.repo.Create(testCtx(t), incumbent); err != nil {
		t.Fatalf("Create(incumbent): %v", err)
	}

	expiresAt := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
	next := newAPIKeyForCreate("next")
	if err := f.repo.Rotate(testCtx(t), incumbent.ID, expiresAt, next); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if next.CreatedAt.IsZero() {
		t.Error("Rotate left the new key's CreatedAt zero")
	}

	gotIncumbent, err := f.repo.GetBySecretHash(testCtx(t), incumbent.SecretHash)
	if err != nil {
		t.Fatalf("GetBySecretHash(incumbent): %v", err)
	}
	if gotIncumbent.ExpiresAt == nil || !gotIncumbent.ExpiresAt.Equal(expiresAt) {
		t.Errorf("incumbent ExpiresAt = %v, want %v", gotIncumbent.ExpiresAt, expiresAt)
	}
	if gotIncumbent.RevokedAt != nil {
		t.Error("Rotate must not revoke the incumbent, only supersede it")
	}

	gotNext, err := f.repo.GetBySecretHash(testCtx(t), next.SecretHash)
	if err != nil {
		t.Fatalf("GetBySecretHash(next): %v", err)
	}
	if gotNext.ExpiresAt != nil || gotNext.RevokedAt != nil {
		t.Error("the newly rotated-in key must be current: no ExpiresAt, no RevokedAt")
	}
}

// TestAPIKeyRepository_Rotate_NoIncumbent covers the first-ever key: Rotate
// with a zero-value incumbent id just inserts, the same as Create.
func TestAPIKeyRepository_Rotate_NoIncumbent(t *testing.T) {
	f := newAPIKeyFixture(t)
	next := newAPIKeyForCreate("first ever")

	if err := f.repo.Rotate(testCtx(t), domain.APIKeyID{}, time.Now(), next); err != nil {
		t.Fatalf("Rotate(no incumbent): %v", err)
	}

	got, err := f.repo.GetBySecretHash(testCtx(t), next.SecretHash)
	if err != nil {
		t.Fatalf("GetBySecretHash: %v", err)
	}
	if got.ExpiresAt != nil {
		t.Error("a Rotate with no incumbent must insert a current key")
	}
}

func TestAPIKeyRepository_Rotate_UnknownIncumbentReturnsNotFound(t *testing.T) {
	f := newAPIKeyFixture(t)
	next := newAPIKeyForCreate("next")

	err := f.repo.Rotate(testCtx(t), domain.NewAPIKeyID(), time.Now(), next)
	if !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Fatalf("Rotate(unknown incumbent) = %v, want ErrAPIKeyNotFound", err)
	}

	// The transaction must have rolled back: next was never inserted.
	if _, err := f.repo.GetBySecretHash(testCtx(t), next.SecretHash); !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Error("a failed Rotate must not leave the new key inserted")
	}
}

func TestAPIKeyRepository_Revoke_Success(t *testing.T) {
	f := newAPIKeyFixture(t)
	key := newAPIKeyForCreate("key")
	if err := f.repo.Create(testCtx(t), key); err != nil {
		t.Fatalf("Create: %v", err)
	}

	revokedAt := time.Now().UTC().Truncate(time.Microsecond)
	if err := f.repo.Revoke(testCtx(t), key.ID, revokedAt); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	got, err := f.repo.GetBySecretHash(testCtx(t), key.SecretHash)
	if err != nil {
		t.Fatalf("GetBySecretHash: %v", err)
	}
	if got.RevokedAt == nil || !got.RevokedAt.Equal(revokedAt) {
		t.Errorf("RevokedAt = %v, want %v", got.RevokedAt, revokedAt)
	}
}

func TestAPIKeyRepository_Revoke_AlreadyRevoked(t *testing.T) {
	f := newAPIKeyFixture(t)
	key := newAPIKeyForCreate("key")
	if err := f.repo.Create(testCtx(t), key); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.repo.Revoke(testCtx(t), key.ID, time.Now()); err != nil {
		t.Fatalf("first Revoke: %v", err)
	}

	err := f.repo.Revoke(testCtx(t), key.ID, time.Now())
	if !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Fatalf("second Revoke = %v, want ErrAPIKeyNotFound (already revoked)", err)
	}
}

func TestAPIKeyRepository_Revoke_UnknownID(t *testing.T) {
	f := newAPIKeyFixture(t)

	err := f.repo.Revoke(testCtx(t), domain.NewAPIKeyID(), time.Now())
	if !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Fatalf("Revoke(unknown) = %v, want ErrAPIKeyNotFound", err)
	}
}

// TestAPIKeyRepository_TouchLastUsed_WritesWhenStale asserts a NULL
// last_used_at (never touched) is always written.
func TestAPIKeyRepository_TouchLastUsed_WritesWhenStale(t *testing.T) {
	f := newAPIKeyFixture(t)
	key := newAPIKeyForCreate("key")
	if err := f.repo.Create(testCtx(t), key); err != nil {
		t.Fatalf("Create: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	staleBefore := now.Add(-15 * time.Minute)
	if err := f.repo.TouchLastUsed(testCtx(t), key.ID, now, staleBefore); err != nil {
		t.Fatalf("TouchLastUsed: %v", err)
	}

	got, err := f.repo.GetBySecretHash(testCtx(t), key.SecretHash)
	if err != nil {
		t.Fatalf("GetBySecretHash: %v", err)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(now) {
		t.Errorf("LastUsedAt = %v, want %v", got.LastUsedAt, now)
	}
}

// TestAPIKeyRepository_TouchLastUsed_NoOpWhenFresh asserts a last_used_at
// newer than staleBefore is left untouched — the throttle that keeps a key
// authenticating repeatedly from writing every request.
func TestAPIKeyRepository_TouchLastUsed_NoOpWhenFresh(t *testing.T) {
	f := newAPIKeyFixture(t)
	key := newAPIKeyForCreate("key")
	if err := f.repo.Create(testCtx(t), key); err != nil {
		t.Fatalf("Create: %v", err)
	}

	firstTouch := time.Now().UTC().Truncate(time.Microsecond)
	if err := f.repo.TouchLastUsed(testCtx(t), key.ID, firstTouch, firstTouch.Add(-15*time.Minute)); err != nil {
		t.Fatalf("first TouchLastUsed: %v", err)
	}

	secondTouch := firstTouch.Add(time.Second)
	staleBefore := secondTouch.Add(-15 * time.Minute)
	if err := f.repo.TouchLastUsed(testCtx(t), key.ID, secondTouch, staleBefore); err != nil {
		t.Fatalf("second TouchLastUsed: %v", err)
	}

	got, err := f.repo.GetBySecretHash(testCtx(t), key.SecretHash)
	if err != nil {
		t.Fatalf("GetBySecretHash: %v", err)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(firstTouch) {
		t.Errorf("LastUsedAt = %v, want unchanged %v (fresh touch must be a no-op)", got.LastUsedAt, firstTouch)
	}
}

func TestNewAPIKeyRepository_NilExecutorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewAPIKeyRepository(nil) did not panic")
		}
	}()
	adapter.NewAPIKeyRepository(nil)
}
