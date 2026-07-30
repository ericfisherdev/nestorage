package adapter_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/notify/adapter"
	"github.com/ericfisherdev/nestorage/internal/notify/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
)

// preferenceFixture bundles an isolated pool and its own PreferenceRepository
// for the gated tests below — dbtest.Harness.NewIsolatedPool must be called
// exactly once per test (mirrors notifyFixture's own doc in this package).
type preferenceFixture struct {
	pool *pgxpool.Pool
	repo *adapter.PreferenceRepository
}

func newPreferenceFixture(t *testing.T) *preferenceFixture {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "preference")
	return &preferenceFixture{pool: pool, repo: adapter.NewPreferenceRepository(pool)}
}

// seedUser inserts a minimal identity.household + identity.member + profile
// row directly — mirrors notifyFixture.seedUser's own rationale (this
// package has no reason to import the identity bounded context's own
// repository just for a test fixture).
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

func TestNewPreferenceRepository_NilExecutorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewPreferenceRepository(nil) did not panic")
		}
	}()
	adapter.NewPreferenceRepository(nil)
}

func TestPreferenceRepository_Upsert_InsertThenFlipRoundTrip(t *testing.T) {
	f := newPreferenceFixture(t)
	userID := f.seedUser(t)
	ctx := testCtx(t)

	insert := domain.Preference{UserID: userID, EventType: domain.EventTypeReturnRequested, Channel: domain.ChannelEmail, Enabled: true}
	if err := f.repo.Upsert(ctx, insert); err != nil {
		t.Fatalf("Upsert(insert): %v", err)
	}

	got, err := f.repo.Get(ctx, userID, domain.EventTypeReturnRequested)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 1 || !got[0].Enabled || got[0].Channel != domain.ChannelEmail {
		t.Fatalf("Get after insert = %+v, want one enabled email row", got)
	}

	flip := domain.Preference{UserID: userID, EventType: domain.EventTypeReturnRequested, Channel: domain.ChannelEmail, Enabled: false}
	if err := f.repo.Upsert(ctx, flip); err != nil {
		t.Fatalf("Upsert(flip): %v", err)
	}

	got, err = f.repo.Get(ctx, userID, domain.EventTypeReturnRequested)
	if err != nil {
		t.Fatalf("Get after flip: %v", err)
	}
	if len(got) != 1 || got[0].Enabled {
		t.Fatalf("Get after flip = %+v, want one disabled row (upsert on the primary key, not a second row)", got)
	}
}

func TestPreferenceRepository_ListForUser_ScopedToUser(t *testing.T) {
	f := newPreferenceFixture(t)
	owner := f.seedUser(t)
	stranger := f.seedUser(t)
	ctx := testCtx(t)

	if err := f.repo.Upsert(ctx, domain.Preference{UserID: owner, EventType: domain.EventTypeReturnRequested, Channel: domain.ChannelEmail, Enabled: true}); err != nil {
		t.Fatalf("Upsert(owner, return_requested): %v", err)
	}
	if err := f.repo.Upsert(ctx, domain.Preference{UserID: owner, EventType: domain.EventTypeItemReturned, Channel: domain.ChannelEmail, Enabled: true}); err != nil {
		t.Fatalf("Upsert(owner, item_returned): %v", err)
	}
	if err := f.repo.Upsert(ctx, domain.Preference{UserID: stranger, EventType: domain.EventTypeReturnRequested, Channel: domain.ChannelEmail, Enabled: true}); err != nil {
		t.Fatalf("Upsert(stranger): %v", err)
	}

	got, err := f.repo.ListForUser(ctx, owner)
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListForUser(owner) returned %d rows, want exactly 2 (never the stranger's own row)", len(got))
	}
	for _, p := range got {
		if p.UserID != owner {
			t.Errorf("ListForUser(owner) returned a row for %v, want only %v", p.UserID, owner)
		}
	}
}

func TestPreferenceRepository_ListForUser_Empty(t *testing.T) {
	f := newPreferenceFixture(t)
	got, err := f.repo.ListForUser(testCtx(t), identity.NewUserID())
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListForUser(no rows) = %+v, want empty slice", got)
	}
}

func TestPreferenceRepository_Get_Empty(t *testing.T) {
	f := newPreferenceFixture(t)
	userID := f.seedUser(t)
	got, err := f.repo.Get(testCtx(t), userID, domain.EventTypeReturnRequested)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Get(no rows) = %+v, want empty slice", got)
	}
}

// TestPreferenceRepository_OnDeleteCascade_RemovesPreferences proves the
// migration's ON DELETE CASCADE: deleting the owning identity.member row leaves no
// orphaned preference row behind.
func TestPreferenceRepository_OnDeleteCascade_RemovesPreferences(t *testing.T) {
	f := newPreferenceFixture(t)
	userID := f.seedUser(t)
	ctx := testCtx(t)

	if err := f.repo.Upsert(ctx, domain.Preference{UserID: userID, EventType: domain.EventTypeReturnRequested, Channel: domain.ChannelEmail, Enabled: true}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if _, err := f.pool.Exec(ctx, `DELETE FROM identity.member WHERE id = $1`, userID.String()); err != nil {
		t.Fatalf("delete identity.member: %v", err)
	}

	got, err := f.repo.ListForUser(ctx, userID)
	if err != nil {
		t.Fatalf("ListForUser after user delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListForUser after owning user deleted = %+v, want empty (ON DELETE CASCADE)", got)
	}
}
