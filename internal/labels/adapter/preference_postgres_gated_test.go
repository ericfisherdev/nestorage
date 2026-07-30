package adapter_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/labels/adapter"
	"github.com/ericfisherdev/nestorage/internal/labels/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
)

// preferenceFixture bundles an isolated pool and its own
// SizePreferenceRepository for the gated tests below —
// dbtest.Harness.NewIsolatedPool must be called exactly once per test
// (mirrors notify/adapter's own preferenceFixture doc).
type preferenceFixture struct {
	pool *pgxpool.Pool
	repo *adapter.SizePreferenceRepository
}

func newPreferenceFixture(t *testing.T) *preferenceFixture {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "labels_preference")
	return &preferenceFixture{pool: pool, repo: adapter.NewSizePreferenceRepository(pool)}
}

// seedUser inserts a minimal identity.household + identity.member + profile
// row directly — this package has no reason to import the identity bounded
// context's own repository just for a test fixture, mirroring notify/adapter's
// own seedUser rationale.
func (f *preferenceFixture) seedUser(t *testing.T) identity.UserID {
	t.Helper()
	id := identity.NewUserID()
	householdID := identity.NewHouseholdID()
	email := id.String() + "@example.test"

	const householdQ = `INSERT INTO identity.household (id, name) VALUES ($1, 'Test Household')`
	if _, err := f.pool.Exec(testCtx(t), householdQ, householdID.String()); err != nil {
		t.Fatalf("seed household: %v", err)
	}
	const memberQ = `INSERT INTO identity.member (id, household_id, display_name, email, password_hash, role) VALUES ($1, $2, 'Test User', $3, 'x', 'adult')`
	if _, err := f.pool.Exec(testCtx(t), memberQ, id.String(), householdID.String(), email); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	const profileQ = `INSERT INTO profile (member_id, color) VALUES ($1, 'indigo')`
	if _, err := f.pool.Exec(testCtx(t), profileQ, id.String()); err != nil {
		t.Fatalf("seed user profile: %v", err)
	}
	return id
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

func TestNewSizePreferenceRepository_NilExecutorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewSizePreferenceRepository(nil) did not panic")
		}
	}()
	adapter.NewSizePreferenceRepository(nil)
}

func TestSizePreferenceRepository_Get_NeverPrinted_ErrPreferenceNotFound(t *testing.T) {
	f := newPreferenceFixture(t)
	userID := f.seedUser(t)

	_, err := f.repo.Get(testCtx(t), userID)
	if !errors.Is(err, domain.ErrPreferenceNotFound) {
		t.Errorf("Get(never printed) = %v, want ErrPreferenceNotFound", err)
	}
}

func TestSizePreferenceRepository_Upsert_InsertThenFlipRoundTrip(t *testing.T) {
	f := newPreferenceFixture(t)
	userID := f.seedUser(t)
	ctx := testCtx(t)

	if err := f.repo.Upsert(ctx, userID, domain.LabelSizeAvery5160); err != nil {
		t.Fatalf("Upsert(insert): %v", err)
	}
	got, err := f.repo.Get(ctx, userID)
	if err != nil {
		t.Fatalf("Get after insert: %v", err)
	}
	if got != domain.LabelSizeAvery5160 {
		t.Errorf("Get after insert = %q, want %q", got, domain.LabelSizeAvery5160)
	}

	if err := f.repo.Upsert(ctx, userID, domain.LabelSizeAvery5163); err != nil {
		t.Fatalf("Upsert(flip): %v", err)
	}
	got, err = f.repo.Get(ctx, userID)
	if err != nil {
		t.Fatalf("Get after flip: %v", err)
	}
	if got != domain.LabelSizeAvery5163 {
		t.Errorf("Get after flip = %q, want %q (upsert on the primary key, not a second row)", got, domain.LabelSizeAvery5163)
	}
}

func TestSizePreferenceRepository_Upsert_ScopedToUser(t *testing.T) {
	f := newPreferenceFixture(t)
	owner := f.seedUser(t)
	stranger := f.seedUser(t)
	ctx := testCtx(t)

	if err := f.repo.Upsert(ctx, owner, domain.LabelSizeAvery5160); err != nil {
		t.Fatalf("Upsert(owner): %v", err)
	}

	_, err := f.repo.Get(ctx, stranger)
	if !errors.Is(err, domain.ErrPreferenceNotFound) {
		t.Errorf("Get(stranger) = %v, want ErrPreferenceNotFound (owner's row must not leak)", err)
	}
}

// TestSizePreferenceRepository_OnDeleteCascade_RemovesPreference proves the
// migration's ON DELETE CASCADE: deleting the owning identity.member row leaves no
// orphaned preference row behind.
func TestSizePreferenceRepository_OnDeleteCascade_RemovesPreference(t *testing.T) {
	f := newPreferenceFixture(t)
	userID := f.seedUser(t)
	ctx := testCtx(t)

	if err := f.repo.Upsert(ctx, userID, domain.LabelSizeAvery5160); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM identity.member WHERE id = $1`, userID.String()); err != nil {
		t.Fatalf("delete identity.member: %v", err)
	}

	_, err := f.repo.Get(ctx, userID)
	if !errors.Is(err, domain.ErrPreferenceNotFound) {
		t.Errorf("Get after owning user deleted = %v, want ErrPreferenceNotFound (ON DELETE CASCADE)", err)
	}
}
