package adapter_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
)

// newTestRepo returns a repository over this package's own derived database,
// freshly reset and migrated, plus the id of the one identity.household
// seeded into it — every identity.member row is NOT NULL on household_id.
// dbtest.Harness.NewIsolatedPool owns the safety rail, the on-demand CREATE
// DATABASE, and the reset/migrate lifecycle. The "identity" suffix must stay
// unique across the repo's gated test packages.
func newTestRepo(t *testing.T) (*adapter.UserRepository, domain.HouseholdID) {
	t.Helper()
	repo, pool, householdID := newTestRepoWithPool(t)
	_ = pool
	return repo, householdID
}

// newTestRepoWithPool is newTestRepo plus the pool itself, for tests that
// need to seed rows outside the repository's own writes (e.g. an
// identity.member row with no profile row, to prove the LEFT JOIN/upsert
// fixes below).
func newTestRepoWithPool(t *testing.T) (*adapter.UserRepository, *pgxpool.Pool, domain.HouseholdID) {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "identity")
	return adapter.NewUserRepository(pool), pool, seedHousehold(t, pool)
}

// seedHousehold inserts a minimal identity.household row directly — every
// gated fixture in this package that seeds a user needs one.
func seedHousehold(t *testing.T, pool *pgxpool.Pool) domain.HouseholdID {
	t.Helper()
	id := domain.NewHouseholdID()
	const q = `INSERT INTO identity.household (id, name) VALUES ($1, 'Test Household')`
	if _, err := pool.Exec(testCtx(t), q, id.String()); err != nil {
		t.Fatalf("seed household: %v", err)
	}
	return id
}

// testCtx returns a per-call context bounded so a slow/unresponsive database
// fails the test rather than hanging it.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func newUser(householdID domain.HouseholdID, email string) *domain.User {
	return &domain.User{
		ID:           domain.NewUserID(),
		HouseholdID:  householdID,
		DisplayName:  "Maya",
		Email:        email,
		PasswordHash: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		Role:         domain.RoleAdult,
		Color:        domain.ColorIndigo,
	}
}

func seedUser(t *testing.T, repo *adapter.UserRepository, householdID domain.HouseholdID, email string) *domain.User {
	t.Helper()
	u := newUser(householdID, email)
	if err := repo.Create(testCtx(t), u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return u
}

// seedAdmin is seedUser's admin-role twin, used by tests that need an
// active admin already present so a later mutation on a DIFFERENT user
// never trips the last-active-admin guard (see postgres_admin_test.go for
// the tests that deliberately DO trip it).
func seedAdmin(t *testing.T, repo *adapter.UserRepository, householdID domain.HouseholdID, email string) *domain.User {
	t.Helper()
	u := newUser(householdID, email)
	u.Role = domain.RoleOwner
	if err := repo.Create(testCtx(t), u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return u
}

func TestCreateAndFindByID(t *testing.T) {
	repo, household := newTestRepo(t)
	u := seedUser(t, repo, household, "maya@example.com")

	if !u.Active {
		t.Error("Create left Active = false, want true (the identity.member.active column defaults true)")
	}
	if u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
		t.Error("Create left CreatedAt/UpdatedAt zero")
	}

	got, err := repo.FindByID(testCtx(t), u.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.ID != u.ID || got.Email != u.Email || got.Role != u.Role || got.Color != u.Color {
		t.Errorf("FindByID = %+v, want a match for %+v", got, u)
	}
}

func TestFindByIDNotFound(t *testing.T) {
	repo, _ := newTestRepo(t)
	_, err := repo.FindByID(testCtx(t), domain.NewUserID())
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("FindByID(unknown) = %v, want ErrUserNotFound", err)
	}
}

func TestFindByEmailIsCaseInsensitive(t *testing.T) {
	repo, household := newTestRepo(t)
	u := seedUser(t, repo, household, "maya@example.com")

	got, err := repo.FindByEmail(testCtx(t), "MAYA@EXAMPLE.COM")
	if err != nil {
		t.Fatalf("FindByEmail (differently cased): %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("FindByEmail(differently cased) = id %v, want %v", got.ID, u.ID)
	}
}

func TestFindByEmailNotFound(t *testing.T) {
	repo, _ := newTestRepo(t)
	_, err := repo.FindByEmail(testCtx(t), "nobody@example.com")
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("FindByEmail(unknown) = %v, want ErrUserNotFound", err)
	}
}

// TestFindByEmail_NoProfileRowStillReadableWithDefaultColor proves
// userColumns' LEFT JOIN: an identity.member row with no matching profile
// row (e.g. a member another app created directly against the shared
// identity schema) must still be findable and loginable, defaulting to
// ColorIndigo — not silently invisible to FindByEmail/FindByID/List while
// Count/HasAnyUser (which query identity.member directly) still count it.
func TestFindByEmail_NoProfileRowStillReadableWithDefaultColor(t *testing.T) {
	_, pool, householdID := newTestRepoWithPool(t)
	repo := adapter.NewUserRepository(pool)

	memberID := domain.NewUserID()
	const memberQ = `
		INSERT INTO identity.member (id, household_id, display_name, email, password_hash, role)
		VALUES ($1, $2, 'No Profile', 'no-profile@example.com', 'x', 'adult')`
	if _, err := pool.Exec(testCtx(t), memberQ, memberID.String(), householdID.String()); err != nil {
		t.Fatalf("seed member without a profile row: %v", err)
	}

	got, err := repo.FindByEmail(testCtx(t), "no-profile@example.com")
	if err != nil {
		t.Fatalf("FindByEmail(no profile row) = %v, want no error", err)
	}
	if got.Color != domain.ColorIndigo {
		t.Errorf("FindByEmail(no profile row).Color = %v, want the default %v", got.Color, domain.ColorIndigo)
	}
}

func TestCreateDuplicateEmailRejectedCaseInsensitively(t *testing.T) {
	repo, household := newTestRepo(t)
	seedUser(t, repo, household, "maya@example.com")

	dup := newUser(household, "MAYA@EXAMPLE.COM")
	err := repo.Create(testCtx(t), dup)
	if !errors.Is(err, domain.ErrDuplicateEmail) {
		t.Errorf("Create(email differing only in case) = %v, want ErrDuplicateEmail", err)
	}
}

func TestList(t *testing.T) {
	repo, household := newTestRepo(t)
	seedUser(t, repo, household, "ivy@example.com")
	seedUser(t, repo, household, "daniel@example.com")

	users, err := repo.List(testCtx(t))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("List returned %d users, want 2", len(users))
	}
}

func TestListEmpty(t *testing.T) {
	repo, _ := newTestRepo(t)
	users, err := repo.List(testCtx(t))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("List on an empty database = %d users, want 0", len(users))
	}
}

func TestUpdate(t *testing.T) {
	repo, household := newTestRepo(t)
	u := seedUser(t, repo, household, "maya@example.com")

	u.DisplayName = "Maya Fisher"
	u.Email = "maya.fisher@example.com"
	u.Role = domain.RoleOwner
	u.Color = domain.ColorTeal
	if err := repo.Update(testCtx(t), u); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := repo.FindByID(testCtx(t), u.ID)
	if err != nil {
		t.Fatalf("FindByID after Update: %v", err)
	}
	if got.DisplayName != "Maya Fisher" || got.Email != "maya.fisher@example.com" ||
		got.Role != domain.RoleOwner || got.Color != domain.ColorTeal {
		t.Errorf("FindByID after Update = %+v, want the updated fields", got)
	}
}

// TestUpdate_NoProfileRowUpserts proves Update's profile write is an upsert,
// not a bare UPDATE: a member with no existing profile row must still get
// its color change persisted, not silently no-op it.
func TestUpdate_NoProfileRowUpserts(t *testing.T) {
	_, pool, householdID := newTestRepoWithPool(t)
	repo := adapter.NewUserRepository(pool)

	memberID := domain.NewUserID()
	const memberQ = `
		INSERT INTO identity.member (id, household_id, display_name, email, password_hash, role)
		VALUES ($1, $2, 'No Profile', 'no-profile@example.com', 'x', 'adult')`
	if _, err := pool.Exec(testCtx(t), memberQ, memberID.String(), householdID.String()); err != nil {
		t.Fatalf("seed member without a profile row: %v", err)
	}

	u := &domain.User{
		ID:          memberID,
		DisplayName: "No Profile",
		Email:       "no-profile@example.com",
		Role:        domain.RoleAdult,
		Color:       domain.ColorTeal,
	}
	if err := repo.Update(testCtx(t), u); err != nil {
		t.Fatalf("Update(no profile row): %v", err)
	}

	got, err := repo.FindByID(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("FindByID after Update: %v", err)
	}
	if got.Color != domain.ColorTeal {
		t.Errorf("Color after Update(no profile row) = %v, want %v (the change must persist, not silently no-op)", got.Color, domain.ColorTeal)
	}
}

func TestUpdateNotFound(t *testing.T) {
	repo, household := newTestRepo(t)
	u := newUser(household, "ghost@example.com")
	u.ID = domain.NewUserID()

	err := repo.Update(testCtx(t), u)
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("Update(unknown id) = %v, want ErrUserNotFound", err)
	}
}

func TestUpdateDuplicateEmail(t *testing.T) {
	repo, household := newTestRepo(t)
	seedUser(t, repo, household, "ivy@example.com")
	daniel := seedUser(t, repo, household, "daniel@example.com")

	daniel.Email = "IVY@EXAMPLE.COM"
	err := repo.Update(testCtx(t), daniel)
	if !errors.Is(err, domain.ErrDuplicateEmail) {
		t.Errorf("Update(email differing only in case from another user) = %v, want ErrDuplicateEmail", err)
	}
}

func TestSetActiveBothDirections(t *testing.T) {
	repo, household := newTestRepo(t)
	u := seedUser(t, repo, household, "maya@example.com")

	if err := repo.SetActive(testCtx(t), u.ID, false); err != nil {
		t.Fatalf("SetActive(false): %v", err)
	}
	got, err := repo.FindByID(testCtx(t), u.ID)
	if err != nil {
		t.Fatalf("FindByID after deactivate: %v", err)
	}
	if got.Active {
		t.Error("FindByID after SetActive(false) = Active true, want false")
	}

	if err := repo.SetActive(testCtx(t), u.ID, true); err != nil {
		t.Fatalf("SetActive(true): %v", err)
	}
	got, err = repo.FindByID(testCtx(t), u.ID)
	if err != nil {
		t.Fatalf("FindByID after reactivate: %v", err)
	}
	if !got.Active {
		t.Error("FindByID after SetActive(true) = Active false, want true")
	}
}

func TestSetActiveNotFound(t *testing.T) {
	repo, _ := newTestRepo(t)
	err := repo.SetActive(testCtx(t), domain.NewUserID(), false)
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("SetActive(unknown id) = %v, want ErrUserNotFound", err)
	}
}

// TestSetRolePromotesAndDemotes seeds a second admin first so demoting the
// member-under-test back to member never trips the last-active-admin guard
// — that guard's rejection path is covered separately in
// postgres_admin_test.go.
func TestSetRolePromotesAndDemotes(t *testing.T) {
	repo, household := newTestRepo(t)
	seedAdmin(t, repo, household, "admin@example.com")
	member := seedUser(t, repo, household, "maya@example.com")

	if err := repo.SetRole(testCtx(t), member.ID, domain.RoleOwner); err != nil {
		t.Fatalf("SetRole(admin): %v", err)
	}
	got, err := repo.FindByID(testCtx(t), member.ID)
	if err != nil {
		t.Fatalf("FindByID after promote: %v", err)
	}
	if got.Role != domain.RoleOwner {
		t.Errorf("Role after promote = %v, want RoleOwner", got.Role)
	}

	if err := repo.SetRole(testCtx(t), member.ID, domain.RoleAdult); err != nil {
		t.Fatalf("SetRole(member): %v", err)
	}
	got, err = repo.FindByID(testCtx(t), member.ID)
	if err != nil {
		t.Fatalf("FindByID after demote: %v", err)
	}
	if got.Role != domain.RoleAdult {
		t.Errorf("Role after demote = %v, want RoleAdult", got.Role)
	}
}

func TestSetRoleNotFound(t *testing.T) {
	repo, _ := newTestRepo(t)
	err := repo.SetRole(testCtx(t), domain.NewUserID(), domain.RoleOwner)
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("SetRole(unknown id) = %v, want ErrUserNotFound", err)
	}
}

func TestSetPasswordHash(t *testing.T) {
	repo, household := newTestRepo(t)
	u := seedUser(t, repo, household, "maya@example.com")

	const newHash = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$bmV3aGFzaA"
	if err := repo.SetPasswordHash(testCtx(t), u.ID, newHash); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}
	got, err := repo.FindByID(testCtx(t), u.ID)
	if err != nil {
		t.Fatalf("FindByID after SetPasswordHash: %v", err)
	}
	if got.PasswordHash != newHash {
		t.Errorf("PasswordHash after SetPasswordHash = %q, want %q", got.PasswordHash, newHash)
	}
}

func TestSetPasswordHashNotFound(t *testing.T) {
	repo, _ := newTestRepo(t)
	err := repo.SetPasswordHash(testCtx(t), domain.NewUserID(), "hash")
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("SetPasswordHash(unknown id) = %v, want ErrUserNotFound", err)
	}
}

func TestCount(t *testing.T) {
	repo, household := newTestRepo(t)

	n, err := repo.Count(testCtx(t))
	if err != nil {
		t.Fatalf("Count on an empty database: %v", err)
	}
	if n != 0 {
		t.Errorf("Count on an empty database = %d, want 0", n)
	}

	seedUser(t, repo, household, "maya@example.com")
	seedUser(t, repo, household, "ivy@example.com")

	n, err = repo.Count(testCtx(t))
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 2 {
		t.Errorf("Count = %d, want 2", n)
	}
}

func TestHasAnyUser(t *testing.T) {
	repo, household := newTestRepo(t)

	has, err := repo.HasAnyUser(testCtx(t))
	if err != nil {
		t.Fatalf("HasAnyUser on an empty database: %v", err)
	}
	if has {
		t.Error("HasAnyUser on an empty database = true, want false")
	}

	seedUser(t, repo, household, "maya@example.com")

	has, err = repo.HasAnyUser(testCtx(t))
	if err != nil {
		t.Fatalf("HasAnyUser: %v", err)
	}
	if !has {
		t.Error("HasAnyUser after seeding a user = false, want true")
	}
}

// seedProfilelessMember inserts an identity.member row directly, with no
// matching profile row — modeling a member provisioned by Nestova (or
// created directly against the shared identity schema) arriving at
// Nestorage for the first time over a shared session (NSTR-117).
func seedProfilelessMember(t *testing.T, pool *pgxpool.Pool, householdID domain.HouseholdID, displayName, email string) domain.UserID {
	t.Helper()
	memberID := domain.NewUserID()
	const memberQ = `
		INSERT INTO identity.member (id, household_id, display_name, email, password_hash, role)
		VALUES ($1, $2, $3, $4, 'x', 'adult')`
	if _, err := pool.Exec(testCtx(t), memberQ, memberID.String(), householdID.String(), displayName, email); err != nil {
		t.Fatalf("seed member without a profile row: %v", err)
	}
	return memberID
}

// TestEnsureProfile_CreatesRowViaNextColor is the NSTR-117 create-if-missing
// proof: a member visible through identity.member (as it would be for a
// member provisioned by Nestova, arriving here for the first time over a
// shared session) but with no profile row gets one created via NextColor
// against colors already used in the household, not merely a display-time
// default.
func TestEnsureProfile_CreatesRowViaNextColor(t *testing.T) {
	repo, pool, household := newTestRepoWithPool(t)

	// Two users already have profile rows using indigo and steel.
	seedUser(t, repo, household, "maya@example.com")
	second := newUser(household, "daniel@example.com")
	second.Color = domain.ColorSteel
	if err := repo.Create(testCtx(t), second); err != nil {
		t.Fatalf("Create(second): %v", err)
	}

	crossAppID := seedProfilelessMember(t, pool, household, "Cross App", "cross-app@example.com")

	got, err := repo.EnsureProfile(testCtx(t), crossAppID)
	if err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}
	if got.Color != domain.ColorTeal {
		t.Errorf("EnsureProfile color = %v, want %v (first unused color in the household)", got.Color, domain.ColorTeal)
	}

	var persisted string
	if err := pool.QueryRow(testCtx(t), "SELECT color FROM profile WHERE member_id = $1", crossAppID.String()).Scan(&persisted); err != nil {
		t.Fatalf("query persisted profile row: %v", err)
	}
	if persisted != domain.ColorTeal.String() {
		t.Errorf("persisted color = %q, want %q", persisted, domain.ColorTeal.String())
	}
}

// TestEnsureProfile_ExistingRowIsUnchanged verifies idempotence: a user who
// already has a profile row is returned exactly as stored, without
// EnsureProfile reassigning a new color.
func TestEnsureProfile_ExistingRowIsUnchanged(t *testing.T) {
	repo, household := newTestRepo(t)
	u := newUser(household, "maya@example.com")
	u.Color = domain.ColorPeri
	if err := repo.Create(testCtx(t), u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.EnsureProfile(testCtx(t), u.ID)
	if err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}
	if got.Color != domain.ColorPeri {
		t.Errorf("EnsureProfile color = %v, want unchanged %v", got.Color, domain.ColorPeri)
	}
}

// TestEnsureProfile_UnknownUser verifies the documented error contract: an
// unknown user id reports ErrUserNotFound, exactly like FindByID.
func TestEnsureProfile_UnknownUser(t *testing.T) {
	repo, _ := newTestRepo(t)
	if _, err := repo.EnsureProfile(testCtx(t), domain.NewUserID()); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("EnsureProfile(unknown) error = %v, want ErrUserNotFound", err)
	}
}

// TestEnsureProfile_ConcurrentDifferentUsers_NoColorCollision proves the
// household advisory lock actually closes the race a naive read-then-insert
// would have: two DIFFERENT profile-less users of the SAME household,
// provisioned concurrently, must not both land on the same "first unused"
// color — profile has no per-household color uniqueness constraint to catch
// that at the database level, so this is the only thing that would.
func TestEnsureProfile_ConcurrentDifferentUsers_NoColorCollision(t *testing.T) {
	repo, pool, household := newTestRepoWithPool(t)

	aID := seedProfilelessMember(t, pool, household, "A", "a@example.com")
	bID := seedProfilelessMember(t, pool, household, "B", "b@example.com")

	var wg sync.WaitGroup
	results := make([]*domain.User, 2)
	errs := make([]error, 2)
	for i, id := range []domain.UserID{aID, bID} {
		wg.Add(1)
		go func(i int, id domain.UserID) {
			defer wg.Done()
			results[i], errs[i] = repo.EnsureProfile(testCtx(t), id)
		}(i, id)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("EnsureProfile(%d): %v", i, err)
		}
	}
	if results[0].Color == results[1].Color {
		t.Errorf("both concurrently provisioned users got color %v, want two distinct colors", results[0].Color)
	}
}
