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

// deviceTokenFixture wires a DeviceTokenRepository and a UserRepository over
// ONE derived database (dbtest.Harness.NewIsolatedPool must be called
// exactly once per test — a second call resets the schema it just built,
// wiping any data already written), so a test can seed the app_user row
// device_token's FK requires and then exercise the repository under test.
type deviceTokenFixture struct {
	pool  *pgxpool.Pool
	repo  *adapter.DeviceTokenRepository
	users *adapter.UserRepository
}

// newDeviceTokenFixture derives this package's shared "identity" database —
// the same suffix every other gated test in this package uses (see
// newTestRepo's own doc); it must stay the one suffix for the whole
// package, not a device-token-specific one.
func newDeviceTokenFixture(t *testing.T) *deviceTokenFixture {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "identity")
	return &deviceTokenFixture{
		pool:  pool,
		repo:  adapter.NewDeviceTokenRepository(pool),
		users: adapter.NewUserRepository(pool),
	}
}

// seedOwner creates and returns the id of a user for device_token's FK.
func (f *deviceTokenFixture) seedOwner(t *testing.T) domain.UserID {
	t.Helper()
	u := newUser("device-owner-" + domain.NewUserID().String() + "@example.com")
	if err := f.users.Create(testCtx(t), u); err != nil {
		t.Fatalf("seed device token owner: %v", err)
	}
	return u.ID
}

func newDeviceToken(userID domain.UserID, name string) *domain.DeviceToken {
	raw, err := domain.GenerateDeviceToken()
	if err != nil {
		panic(err)
	}
	return &domain.DeviceToken{
		ID:        domain.NewDeviceTokenID(),
		UserID:    userID,
		TokenHash: domain.HashDeviceToken(raw),
		Name:      name,
	}
}

func TestDeviceTokenRepository_CreateAndGetByTokenHash(t *testing.T) {
	f := newDeviceTokenFixture(t)
	userID := f.seedOwner(t)
	token := newDeviceToken(userID, "Maya's phone")

	if err := f.repo.Create(testCtx(t), token); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if token.CreatedAt.IsZero() {
		t.Error("Create left CreatedAt zero")
	}

	got, err := f.repo.GetByTokenHash(testCtx(t), token.TokenHash)
	if err != nil {
		t.Fatalf("GetByTokenHash: %v", err)
	}
	if got.ID != token.ID || got.UserID != userID || got.Name != "Maya's phone" {
		t.Errorf("GetByTokenHash = %+v, want it to match the created token", got)
	}
	if !got.Active() {
		t.Error("a freshly created token must be Active")
	}
}

func TestDeviceTokenRepository_Create_UnknownUserRejected(t *testing.T) {
	f := newDeviceTokenFixture(t)
	token := newDeviceToken(domain.NewUserID(), "phone")

	err := f.repo.Create(testCtx(t), token)
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("Create(unknown user) = %v, want ErrUserNotFound", err)
	}
}

func TestDeviceTokenRepository_GetByTokenHash_UnknownHash(t *testing.T) {
	f := newDeviceTokenFixture(t)

	_, err := f.repo.GetByTokenHash(testCtx(t), "nonexistent-hash")
	if !errors.Is(err, domain.ErrDeviceTokenNotFound) {
		t.Fatalf("GetByTokenHash(unknown) = %v, want ErrDeviceTokenNotFound", err)
	}
}

// TestDeviceTokenRepository_GetByTokenHash_ReturnsRevokedRow asserts a
// revoked token is still returned by GetByTokenHash (with RevokedAt set)
// rather than reported as not-found — the port's documented contract for
// distinguishing "unknown" from "known but revoked".
func TestDeviceTokenRepository_GetByTokenHash_ReturnsRevokedRow(t *testing.T) {
	f := newDeviceTokenFixture(t)
	userID := f.seedOwner(t)
	token := newDeviceToken(userID, "phone")
	if err := f.repo.Create(testCtx(t), token); err != nil {
		t.Fatalf("Create: %v", err)
	}
	revokedAt := time.Now().UTC().Truncate(time.Microsecond)
	if err := f.repo.Revoke(testCtx(t), userID, token.ID, revokedAt); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	got, err := f.repo.GetByTokenHash(testCtx(t), token.TokenHash)
	if err != nil {
		t.Fatalf("GetByTokenHash: %v", err)
	}
	if got.Active() {
		t.Error("GetByTokenHash on a revoked token reported Active, want revoked")
	}
	if got.RevokedAt == nil || !got.RevokedAt.Equal(revokedAt) {
		t.Errorf("RevokedAt = %v, want %v", got.RevokedAt, revokedAt)
	}
}

// TestDeviceTokenRepository_Revoke_ScopedByUser is the automated equivalent
// of this ticket's "revoking one device does not affect that user's other
// devices" criterion: another user's id cannot revoke this token.
func TestDeviceTokenRepository_Revoke_ScopedByUser(t *testing.T) {
	f := newDeviceTokenFixture(t)
	owner := f.seedOwner(t)
	otherUser := f.seedOwner(t)
	token := newDeviceToken(owner, "phone")
	if err := f.repo.Create(testCtx(t), token); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err := f.repo.Revoke(testCtx(t), otherUser, token.ID, time.Now())
	if !errors.Is(err, domain.ErrDeviceTokenNotFound) {
		t.Fatalf("Revoke(wrong user) = %v, want ErrDeviceTokenNotFound", err)
	}

	got, err := f.repo.GetByTokenHash(testCtx(t), token.TokenHash)
	if err != nil {
		t.Fatalf("GetByTokenHash: %v", err)
	}
	if !got.Active() {
		t.Error("Revoke(wrong user) must leave the token active")
	}
}

func TestDeviceTokenRepository_Revoke_AlreadyRevoked(t *testing.T) {
	f := newDeviceTokenFixture(t)
	userID := f.seedOwner(t)
	token := newDeviceToken(userID, "phone")
	if err := f.repo.Create(testCtx(t), token); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.repo.Revoke(testCtx(t), userID, token.ID, time.Now()); err != nil {
		t.Fatalf("first Revoke: %v", err)
	}

	err := f.repo.Revoke(testCtx(t), userID, token.ID, time.Now())
	if !errors.Is(err, domain.ErrDeviceTokenNotFound) {
		t.Fatalf("second Revoke = %v, want ErrDeviceTokenNotFound (already revoked)", err)
	}
}

// TestDeviceTokenRepository_RevokeAllForUser_LeavesOtherUsersUntouched is the
// automated equivalent of this ticket's "revoking one device does not
// affect that user's other devices" criterion, at the RevokeAllForUser
// level NSTR-21's deactivation flow calls.
func TestDeviceTokenRepository_RevokeAllForUser_LeavesOtherUsersUntouched(t *testing.T) {
	f := newDeviceTokenFixture(t)
	target := f.seedOwner(t)
	other := f.seedOwner(t)

	targetA := newDeviceToken(target, "phone A")
	targetB := newDeviceToken(target, "phone B")
	otherToken := newDeviceToken(other, "phone C")
	for _, tok := range []*domain.DeviceToken{targetA, targetB, otherToken} {
		if err := f.repo.Create(testCtx(t), tok); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	n, err := f.repo.RevokeAllForUser(testCtx(t), target, time.Now())
	if err != nil {
		t.Fatalf("RevokeAllForUser: %v", err)
	}
	if n != 2 {
		t.Errorf("RevokeAllForUser revoked %d tokens, want 2", n)
	}

	gotOther, err := f.repo.GetByTokenHash(testCtx(t), otherToken.TokenHash)
	if err != nil {
		t.Fatalf("GetByTokenHash(other): %v", err)
	}
	if !gotOther.Active() {
		t.Error("RevokeAllForUser must not touch another user's token")
	}
}

func TestDeviceTokenRepository_RevokeAllForUser_NothingToRevokeIsNotAnError(t *testing.T) {
	f := newDeviceTokenFixture(t)
	userID := f.seedOwner(t)

	n, err := f.repo.RevokeAllForUser(testCtx(t), userID, time.Now())
	if err != nil {
		t.Errorf("RevokeAllForUser(no tokens) = %v, want nil", err)
	}
	if n != 0 {
		t.Errorf("RevokeAllForUser(no tokens) = %d, want 0", n)
	}
}

// TestDeviceTokenRepository_TouchLastUsed_WritesWhenStale asserts a NULL
// last_used_at (never touched) is always written.
func TestDeviceTokenRepository_TouchLastUsed_WritesWhenStale(t *testing.T) {
	f := newDeviceTokenFixture(t)
	userID := f.seedOwner(t)
	token := newDeviceToken(userID, "phone")
	if err := f.repo.Create(testCtx(t), token); err != nil {
		t.Fatalf("Create: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	staleBefore := now.Add(-15 * time.Minute)
	if err := f.repo.TouchLastUsed(testCtx(t), token.ID, now, staleBefore); err != nil {
		t.Fatalf("TouchLastUsed: %v", err)
	}

	got, err := f.repo.GetByTokenHash(testCtx(t), token.TokenHash)
	if err != nil {
		t.Fatalf("GetByTokenHash: %v", err)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(now) {
		t.Errorf("LastUsedAt = %v, want %v", got.LastUsedAt, now)
	}
}

// TestDeviceTokenRepository_TouchLastUsed_NoOpWhenFresh asserts a
// last_used_at newer than staleBefore is left untouched — the throttle
// that keeps a device authenticating repeatedly from writing every request.
func TestDeviceTokenRepository_TouchLastUsed_NoOpWhenFresh(t *testing.T) {
	f := newDeviceTokenFixture(t)
	userID := f.seedOwner(t)
	token := newDeviceToken(userID, "phone")
	if err := f.repo.Create(testCtx(t), token); err != nil {
		t.Fatalf("Create: %v", err)
	}

	firstTouch := time.Now().UTC().Truncate(time.Microsecond)
	if err := f.repo.TouchLastUsed(testCtx(t), token.ID, firstTouch, firstTouch.Add(-15*time.Minute)); err != nil {
		t.Fatalf("first TouchLastUsed: %v", err)
	}

	// A second touch a moment later, with a staleBefore that firstTouch has
	// NOT aged past, must be a no-op.
	secondTouch := firstTouch.Add(time.Second)
	staleBefore := secondTouch.Add(-15 * time.Minute)
	if err := f.repo.TouchLastUsed(testCtx(t), token.ID, secondTouch, staleBefore); err != nil {
		t.Fatalf("second TouchLastUsed: %v", err)
	}

	got, err := f.repo.GetByTokenHash(testCtx(t), token.TokenHash)
	if err != nil {
		t.Fatalf("GetByTokenHash: %v", err)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(firstTouch) {
		t.Errorf("LastUsedAt = %v, want unchanged %v (fresh touch must be a no-op)", got.LastUsedAt, firstTouch)
	}
}

// TestDeviceTokenRepository_ListByUser_NewestFirst asserts list ordering.
func TestDeviceTokenRepository_ListByUser_NewestFirst(t *testing.T) {
	f := newDeviceTokenFixture(t)
	userID := f.seedOwner(t)

	first := newDeviceToken(userID, "first")
	if err := f.repo.Create(testCtx(t), first); err != nil {
		t.Fatalf("Create(first): %v", err)
	}
	second := newDeviceToken(userID, "second")
	if err := f.repo.Create(testCtx(t), second); err != nil {
		t.Fatalf("Create(second): %v", err)
	}

	got, err := f.repo.ListByUser(testCtx(t), userID)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByUser returned %d tokens, want 2", len(got))
	}
	if got[0].ID != second.ID || got[1].ID != first.ID {
		t.Error("ListByUser did not return newest first")
	}
}

func TestDeviceTokenRepository_ListByUser_EmptyIsNotAnError(t *testing.T) {
	f := newDeviceTokenFixture(t)
	userID := f.seedOwner(t)

	got, err := f.repo.ListByUser(testCtx(t), userID)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByUser(no tokens) returned %d, want 0", len(got))
	}
}

func TestNewDeviceTokenRepository_NilExecutorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewDeviceTokenRepository(nil) did not panic")
		}
	}()
	adapter.NewDeviceTokenRepository(nil)
}
