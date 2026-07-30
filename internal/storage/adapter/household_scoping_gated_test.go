package adapter_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
	"github.com/ericfisherdev/nestorage/internal/storage/adapter"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// crossHouseholdForeignKeyViolation is the SQLSTATE (23503) every test below
// that expects a bare foreign-key rejection checks for, mirroring
// isForeignKeyViolation's own code (postgres.go) — unexported there, so this
// package (adapter_test, an external test package) cannot call it directly
// and checks the SQLSTATE itself instead, exactly as the ticket's own brief
// allows.
const crossHouseholdForeignKeyViolation = "23503"

// isCrossHouseholdForeignKeyViolation reports whether err is a foreign-key
// violation of any kind, the external-package mirror of
// isForeignKeyViolation/isPgConstraint (bin_postgres.go).
func isCrossHouseholdForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == crossHouseholdForeignKeyViolation
}

// householdScopingFixture wires Location/Bin/Item repositories over ONE
// derived database seeded with TWO households and one member under each —
// distinct from every other fixture in this package (locationFixture,
// binFixture, itemFixture), which each seed exactly one household. This is
// NSTR-122's own cross-household proof: every composite tenant FK the
// migration added must reject a row that names a real, existing reference
// belonging to the WRONG household, with the exact same sentinel it already
// returns for a reference that does not exist at all (see the migration's
// own "a caller cannot distinguish 'unknown' from 'exists in another
// household'" comment) — never a new, more specific error. Shares the
// "storage" suffix (see binFixture's own doc for why splitting it would
// defeat the harness's isolation).
type householdScopingFixture struct {
	pool      *pgxpool.Pool
	locations *adapter.LocationRepository
	bins      *adapter.BinRepository
	items     *adapter.ItemRepository
	users     *identityadapter.UserRepository

	householdA, householdB identity.HouseholdID
	memberA, memberB       identity.UserID
}

func newHouseholdScopingFixture(t *testing.T) *householdScopingFixture {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "storage")
	f := &householdScopingFixture{
		pool:      pool,
		locations: adapter.NewLocationRepository(pool),
		bins:      adapter.NewBinRepository(pool),
		items:     adapter.NewItemRepository(pool),
		users:     identityadapter.NewUserRepository(pool),
	}
	f.householdA = seedHousehold(t, pool)
	f.householdB = seedHousehold(t, pool)
	f.memberA = f.seedMember(t, f.householdA)
	f.memberB = f.seedMember(t, f.householdB)
	return f
}

// seedMember creates and returns the id of a member belonging to household —
// bin/location/item's created_by (and, transitively, owner_id/held_by) FKs
// all resolve against this member's own household, which is the entire
// mechanism these tests exercise.
func (f *householdScopingFixture) seedMember(t *testing.T, household identity.HouseholdID) identity.UserID {
	t.Helper()
	u := &identity.User{
		ID:           identity.NewUserID(),
		HouseholdID:  household,
		DisplayName:  "Household Member",
		Email:        "household-scoping-" + identity.NewUserID().String() + "@example.com",
		PasswordHash: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		Role:         identity.RoleAdult,
		Color:        identity.ColorIndigo,
	}
	if err := f.users.Create(testCtx(t), u); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	return u.ID
}

// seedLocation creates and returns the id of a location under household,
// created by createdBy.
func (f *householdScopingFixture) seedLocation(t *testing.T, household identity.HouseholdID, createdBy identity.UserID) domain.LocationID {
	t.Helper()
	loc := &domain.Location{ID: domain.NewLocationID(), HouseholdID: household, Name: "Garage", CreatedBy: createdBy}
	if err := f.locations.Create(testCtx(t), loc); err != nil {
		t.Fatalf("seed location: %v", err)
	}
	return loc.ID
}

// seedBin creates and returns the id of a public bin under household, in
// location, created by createdBy.
func (f *householdScopingFixture) seedBin(t *testing.T, household identity.HouseholdID, location domain.LocationID, createdBy identity.UserID, code string) domain.BinID {
	t.Helper()
	id := domain.NewBinID()
	b := &domain.Bin{
		ID: id, HouseholdID: household, Code: code, Name: "Bin " + code,
		LocationID: location, CreatedBy: createdBy, Visibility: domain.VisibilityPublic,
	}
	if err := f.bins.Create(testCtx(t), b); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
	return id
}

// TestBinRepository_Create_SameCodeAcrossHouseholds proves bin_code_uniq is
// now UNIQUE (household_id, code): the same label code, created under two
// different households, must not collide.
//
// The two bins this test creates share a bare code across households on
// purpose — that is the very thing under test — but leaving them in place
// would break this package's OWN harness teardown: dbtest.Harness resets
// every derived database's schema back to zero between tests by running
// every migration's Down section (including 00018's, which re-instates the
// OLD single-column UNIQUE (code) before dropping household_id), and a
// leftover cross-household code collision makes that ALTER TABLE fail,
// poisoning every later gated test sharing this package's "storage"
// database for the rest of the run. t.Cleanup below runs before the
// harness's own registered Reset (Go's Cleanup stack is LIFO, and the
// harness's was registered first, inside NewIsolatedPool), removing both
// rows before that Reset ever runs.
func TestBinRepository_Create_SameCodeAcrossHouseholds(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	locB := f.seedLocation(t, f.householdB, f.memberB)

	binA := &domain.Bin{
		ID: domain.NewBinID(), HouseholdID: f.householdA, Code: "SHARED1", Name: "Bin A",
		LocationID: locA, CreatedBy: f.memberA, Visibility: domain.VisibilityPublic,
	}
	binB := &domain.Bin{
		ID: domain.NewBinID(), HouseholdID: f.householdB, Code: "SHARED1", Name: "Bin B",
		LocationID: locB, CreatedBy: f.memberB, Visibility: domain.VisibilityPublic,
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM bin WHERE id = ANY($1)`, []string{binA.ID.String(), binB.ID.String()})
	})

	if err := f.bins.Create(testCtx(t), binA); err != nil {
		t.Fatalf("Create(household A): %v", err)
	}
	if err := f.bins.Create(testCtx(t), binB); err != nil {
		t.Errorf("Create(household B, same code as household A) = %v, want nil (bin_code_uniq is per-household)", err)
	}
}

// TestBinRepository_Create_CrossHouseholdLocationRejected proves the
// (household_id, location_id) composite FK: a bin claiming household A but
// pointing at a location that belongs to household B is rejected with the
// SAME sentinel bin_location_id_fkey already maps an unknown location_id to
// — a caller cannot distinguish "unknown" from "exists in another
// household".
func TestBinRepository_Create_CrossHouseholdLocationRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locB := f.seedLocation(t, f.householdB, f.memberB)

	bin := &domain.Bin{
		ID: domain.NewBinID(), HouseholdID: f.householdA, Code: "XHH-LOC", Name: "Cross-household bin",
		LocationID: locB, CreatedBy: f.memberA, Visibility: domain.VisibilityPublic,
	}
	err := f.bins.Create(testCtx(t), bin)
	if !errors.Is(err, domain.ErrLocationNotFound) {
		t.Errorf("Create(household A bin, household B location) = %v, want ErrLocationNotFound", err)
	}
}

// TestItemRepository_Create_CrossHouseholdBinRejected proves the
// (household_id, current_bin_id) composite FK: an item claiming household A
// but pointing at a bin that belongs to household B is rejected with the
// SAME sentinel item_current_bin_id_fkey already maps an unknown
// current_bin_id to.
func TestItemRepository_Create_CrossHouseholdBinRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locB := f.seedLocation(t, f.householdB, f.memberB)
	binB := f.seedBin(t, f.householdB, locB, f.memberB, "XHH-BIN")

	it := &domain.Item{
		ID: domain.NewItemID(), HouseholdID: f.householdA, Name: "Cross-household item", Quantity: 1,
		CurrentBinID: &binB, CreatedBy: f.memberA,
	}
	err := f.items.Create(testCtx(t), it)
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("Create(household A item, household B bin) = %v, want ErrBinNotFound", err)
	}
}

// TestLocationRepository_Create_CrossHouseholdParentRejected proves the
// (household_id, parent_id) composite self-referential FK: a child location
// claiming household A but naming a parent that belongs to household B is
// rejected. LocationRepository.Create maps no per-constraint sentinel today
// (only Delete does — see LocationRepository.Delete's own doc), so this
// only asserts the foreign-key-violation shape (SQLSTATE 23503), matching
// this file's own isForeignKeyViolation/isPgConstraint convention
// (unexported in package adapter, so re-derived here for this external test
// package).
func TestLocationRepository_Create_CrossHouseholdParentRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	parentB := f.seedLocation(t, f.householdB, f.memberB)

	child := &domain.Location{
		ID: domain.NewLocationID(), HouseholdID: f.householdA, Name: "Cross-household child",
		ParentID: &parentB, CreatedBy: f.memberA,
	}
	err := f.locations.Create(testCtx(t), child)
	if !isCrossHouseholdForeignKeyViolation(err) {
		t.Errorf("Create(household A child, household B parent) = %v, want a foreign-key-violation error (location_parent_id_fkey)", err)
	}
}

// TestBinRepository_Create_CrossHouseholdCreatedByRejected proves the
// (household_id, created_by) composite FK into identity.member: a bin
// claiming household A but created by a member who belongs to household B
// is rejected with the SAME sentinel bin_created_by_fkey already maps an
// unknown created_by to.
func TestBinRepository_Create_CrossHouseholdCreatedByRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)

	bin := &domain.Bin{
		ID: domain.NewBinID(), HouseholdID: f.householdA, Code: "XHH-CREATOR", Name: "Cross-household creator",
		LocationID: locA, CreatedBy: f.memberB, Visibility: domain.VisibilityPublic,
	}
	err := f.bins.Create(testCtx(t), bin)
	if !errors.Is(err, identity.ErrUserNotFound) {
		t.Errorf("Create(household A bin, household B created_by) = %v, want identity.ErrUserNotFound", err)
	}
}
