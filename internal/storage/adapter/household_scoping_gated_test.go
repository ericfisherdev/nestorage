package adapter_test

import (
	"context"
	"errors"
	"testing"
	"time"

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
//
// NSTR-131 extends this fixture (rather than standing up a parallel one)
// with ItemLinkRepository/ItemEventRepository/ReturnRequestRepository and
// their own seed helpers below, so the leak suite's own richer aggregate
// shape — a bin holding an item, a link on that item, an event on that item,
// and a return request against it — is reachable from the SAME
// two-household setup every cross-household test in this file shares. See
// the manifest comment above TestLocationRepository_FindVisibleByID_CrossHouseholdRejected
// for the diffable method -> test mapping this addition covers.
type householdScopingFixture struct {
	pool      *pgxpool.Pool
	locations *adapter.LocationRepository
	bins      *adapter.BinRepository
	items     *adapter.ItemRepository
	links     *adapter.ItemLinkRepository
	events    *adapter.ItemEventRepository
	requests  *adapter.ReturnRequestRepository
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
		links:     adapter.NewItemLinkRepository(pool),
		events:    adapter.NewItemEventRepository(pool),
		requests:  adapter.NewReturnRequestRepository(pool),
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

// seedItem creates and returns the id of an item sitting in bin, under
// household, created by createdBy — the same shape seedBin/seedLocation
// already follow, added so the leak suite below can seed the richer
// aggregate (an item, then a link/event/request hanging off it) the AC
// requires without duplicating itemFixture's own newItem helper across
// packages.
func (f *householdScopingFixture) seedItem(t *testing.T, household identity.HouseholdID, bin domain.BinID, createdBy identity.UserID, name string) domain.ItemID {
	t.Helper()
	it := &domain.Item{
		ID: domain.NewItemID(), HouseholdID: household, Name: name, Quantity: 1,
		CurrentBinID: &bin, CreatedBy: createdBy,
	}
	if err := f.items.Create(testCtx(t), it); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	return it.ID
}

// seedItemLink creates and returns the id of a link at position 0 on item,
// scoped to household — ItemLinkRepository.Create's own household-scoped
// EXISTS guard (item_link_postgres.go) is exercised by every leak test that
// seeds one, not just this helper.
func (f *householdScopingFixture) seedItemLink(t *testing.T, household identity.HouseholdID, item domain.ItemID) domain.ItemLinkID {
	t.Helper()
	l := &domain.ItemLink{ID: domain.NewItemLinkID(), ItemID: item, Label: "Manual", URL: "https://example.com/manual", Position: 0}
	if err := f.links.Create(testCtx(t), household, l); err != nil {
		t.Fatalf("seed item link: %v", err)
	}
	return l.ID
}

// seedItemEvent appends an EventCreated event attributed to actorID (a
// member of household) against item, mirroring the actor.HouseholdID
// assignment item_event_postgres_gated_test.go's own itemEventFixture uses
// (NewUserPrincipal does not set HouseholdID itself — every caller sets it
// explicitly, see identity.Principal's own doc). EventCreated is the
// simplest valid event shape (no bin/location snapshot fields required),
// sufficient for ListByItem's own leak test; ListByBin's needs
// seedBinItemEvent below instead, since EventCreated leaves bin_id NULL.
func (f *householdScopingFixture) seedItemEvent(t *testing.T, household identity.HouseholdID, actorID identity.UserID, item domain.ItemID, itemName string) domain.ItemEventID {
	t.Helper()
	actor := identity.NewUserPrincipal(actorID, identity.RoleAdult, "Household Member")
	actor.HouseholdID = household
	e := domain.NewItemEvent(domain.NewItemEventID(), item, itemName, domain.EventCreated, actor)
	if err := f.events.Append(testCtx(t), &e); err != nil {
		t.Fatalf("seed item event: %v", err)
	}
	return e.ID
}

// seedBinItemEvent appends an EventAdded event naming bin, the shape
// ItemEventRepository.ListByBin's own leak test needs: EventCreated (see
// seedItemEvent above) leaves bin_id NULL, which would make ListByBin
// return empty regardless of whether the household predicate is present —
// proving nothing about scoping. EventAdded's BinID/BinLabel pairing is
// exactly what ListByBin's WHERE bin_id = $1 needs to match on.
func (f *householdScopingFixture) seedBinItemEvent(t *testing.T, household identity.HouseholdID, actorID identity.UserID, item domain.ItemID, itemName string, bin domain.BinID) domain.ItemEventID {
	t.Helper()
	actor := identity.NewUserPrincipal(actorID, identity.RoleAdult, "Household Member")
	actor.HouseholdID = household
	e := domain.NewItemEvent(domain.NewItemEventID(), item, itemName, domain.EventAdded, actor)
	e.BinID, e.BinLabel = &bin, "Bin"
	if err := f.events.Append(testCtx(t), &e); err != nil {
		t.Fatalf("seed bin item event: %v", err)
	}
	return e.ID
}

// seedReturnRequest creates and returns the id of an open return request
// against item, under household. ReturnRequestRepository.Create carries no
// "item must be checked out" invariant of its own (that rule lives in
// app.ReturnRequestService — see domain/return_request.go's own doc and
// return_request_postgres.go's Create, which only enforces the row's three
// foreign keys plus return_request_open_uniq), so item need not actually be
// held for this to be a valid row.
func (f *householdScopingFixture) seedReturnRequest(t *testing.T, household identity.HouseholdID, item domain.ItemID, requesterID, holderID identity.UserID) domain.ReturnRequestID {
	t.Helper()
	req := &domain.ReturnRequest{
		ID: domain.NewReturnRequestID(), HouseholdID: household, ItemID: item,
		RequesterID: requesterID, HolderID: holderID, Status: domain.ReturnRequestStatusOpen,
	}
	if err := f.requests.Create(testCtx(t), req); err != nil {
		t.Fatalf("seed return request: %v", err)
	}
	return req.ID
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

// TestBinRepository_FindVisibleByCode_ScopedToHouseholdEvenWhenCodesCollide
// proves FindVisibleByCode's own household_id predicate: once two households
// share the same code (bin_code_uniq is now per-household, see the test
// above), a viewer in household A must still resolve household A's bin and a
// viewer in household B must still resolve household B's — never the other,
// and never a non-deterministic pick between the two rows Postgres could
// otherwise return for the bare `code = $1` this query used to run before
// this household predicate was added.
func TestBinRepository_FindVisibleByCode_ScopedToHouseholdEvenWhenCodesCollide(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	locB := f.seedLocation(t, f.householdB, f.memberB)

	binA := f.seedBin(t, f.householdA, locA, f.memberA, "COLLIDE")
	binB := f.seedBin(t, f.householdB, locB, f.memberB, "COLLIDE")
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM bin WHERE id = ANY($1)`, []string{binA.String(), binB.String()})
	})

	viewerA := identity.NewUserPrincipal(f.memberA, identity.RoleAdult, "Household A Viewer")
	viewerA.HouseholdID = f.householdA
	viewerB := identity.NewUserPrincipal(f.memberB, identity.RoleAdult, "Household B Viewer")
	viewerB.HouseholdID = f.householdB

	gotA, err := f.bins.FindVisibleByCode(testCtx(t), viewerA, "COLLIDE")
	if err != nil {
		t.Fatalf("FindVisibleByCode(household A viewer): %v", err)
	}
	if gotA.ID != binA {
		t.Errorf("FindVisibleByCode(household A viewer) = bin %v, want household A's own bin %v", gotA.ID, binA)
	}

	gotB, err := f.bins.FindVisibleByCode(testCtx(t), viewerB, "COLLIDE")
	if err != nil {
		t.Fatalf("FindVisibleByCode(household B viewer): %v", err)
	}
	if gotB.ID != binB {
		t.Errorf("FindVisibleByCode(household B viewer) = bin %v, want household B's own bin %v", gotB.ID, binB)
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

// ---------------------------------------------------------------------------
// NSTR-131: the two-household LEAK suite below.
//
// Every test above this line proves NSTR-122's write-path FK story: a row
// that NAMES a cross-household reference is rejected. Everything below
// proves the complementary, read-and-mutate-path story the ticket's own AC
// requires: household B's principal/householdID must not be able to READ,
// LIST, SEARCH, MUTATE, or DELETE a row that simply BELONGS to household A,
// reached by its own real, valid id — no cross-household reference named
// anywhere in the request.
//
// Manifest — one entry per exported method on every repository interface
// this ticket's plan names, mapping method name to the test that proves it.
// Kept as a single block (not scattered per-repository) so a reviewer can
// diff this list against each repository interface's method list in one
// pass, per the ticket's own "one enumerated test entry per method" AC.
//
// LocationRepository (postgres.go):
//
//	Create           -> excluded; Create takes a household from the caller's
//	                    own *Location value, never resolves a household by an
//	                    existing id, so there is no cross-household READ/
//	                    MUTATE-by-id vector to prove here — see
//	                    TestLocationRepository_Create_CrossHouseholdParentRejected
//	                    above for its own cross-household FK proof instead.
//	FindVisibleByID -> TestLocationRepository_FindVisibleByID_CrossHouseholdRejected
//	List             -> TestLocationRepository_List_ExcludesOtherHousehold
//	Rename           -> TestLocationRepository_Rename_CrossHouseholdRejected
//	Delete           -> TestLocationRepository_Delete_CrossHouseholdRejected
//
// BinRepository (bin_postgres.go):
//
//	Create                -> excluded, same reasoning as LocationRepository.Create
//	                         above — see TestBinRepository_Create_CrossHouseholdLocationRejected/
//	                         TestBinRepository_Create_CrossHouseholdCreatedByRejected above.
//	FindVisibleByID       -> TestBinRepository_FindVisibleByID_CrossHouseholdRejected
//	FindVisibleByCode     -> covered above, see TestBinRepository_FindVisibleByCode_ScopedToHouseholdEvenWhenCodesCollide
//	ListVisible           -> TestBinRepository_ListVisible_ExcludesOtherHousehold
//	ListVisibleByLocation -> TestBinRepository_ListVisibleByLocation_CrossHouseholdLocationEmpty
//	Update                -> TestBinRepository_Update_CrossHouseholdRejected
//	UpdateVisibility      -> TestBinRepository_UpdateVisibility_CrossHouseholdRejected
//	Delete                -> TestBinRepository_Delete_CrossHouseholdRejected
//	GetForUpdate          -> TestBinRepository_GetForUpdate_CrossHouseholdRejected
//	Move                  -> TestBinRepository_Move_CrossHouseholdRejected
//
// ItemRepository (item_postgres.go, item_search_postgres.go):
//
//	Create            -> excluded, same reasoning as LocationRepository.Create
//	                     above — see TestItemRepository_Create_CrossHouseholdBinRejected above.
//	Get               -> TestItemRepository_Get_CrossHouseholdRejected
//	GetForUpdate      -> TestItemRepository_GetForUpdate_CrossHouseholdRejected
//	Update            -> TestItemRepository_Update_CrossHouseholdRejected
//	Move              -> TestItemRepository_Move_CrossHouseholdRejected
//	ListByBin         -> TestItemRepository_ListByBin_CrossHouseholdBinEmpty
//	ListVisible       -> TestItemRepository_ListVisible_ExcludesOtherHousehold
//	CountsByBin       -> TestItemRepository_CountsByBin_ExcludesOtherHouseholdBin
//	Delete            -> TestItemRepository_Delete_CrossHouseholdRejected
//	FindVisibleDetail -> TestItemRepository_FindVisibleDetail_CrossHouseholdRejected
//	SearchVisible     -> TestItemRepository_SearchVisible_ExcludesOtherHousehold
//	ListIDsByBin      -> TestItemRepository_ListIDsByBin_CrossHouseholdBinEmpty
//
// ItemLinkRepository (item_link_postgres.go):
//
//	Create       -> TestItemLinkRepository_Create_CrossHouseholdItemRejected
//	Update       -> TestItemLinkRepository_Update_CrossHouseholdRejected
//	Delete       -> TestItemLinkRepository_Delete_CrossHouseholdRejected
//	ListByItem   -> TestItemLinkRepository_ListByItem_CrossHouseholdEmpty
//	NextPosition -> TestItemLinkRepository_NextPosition_CrossHouseholdZero
//
// ItemEventRepository (item_event_postgres.go) — Append is not listed: it
// takes no viewer/householdID argument at all (household_id comes from the
// actor's own Principal, see NewItemEvent's own doc), so there is no
// cross-household call shape to construct in the first place:
//
//	ListByItem -> TestItemEventRepository_ListByItem_CrossHouseholdEmpty
//	ListByBin  -> TestItemEventRepository_ListByBin_CrossHouseholdEmpty
//
// ReturnRequestRepository (return_request_postgres.go) — Create is not
// listed for the same reason as ItemEventRepository.Append: it takes no
// viewer/householdID argument to spoof, only r.HouseholdID on the row being
// inserted, which is NSTR-122's own write-path FK territory (covered by
// this file's Create_CrossHousehold* tests above, not this manifest):
//
//	ListByItem         -> TestReturnRequestRepository_ListByItem_CrossHouseholdEmpty
//	Cancel             -> TestReturnRequestRepository_Cancel_CrossHouseholdRejected
//	FulfillOpenForItem -> TestReturnRequestRepository_FulfillOpenForItem_CrossHouseholdNoop
//
// ---------------------------------------------------------------------------

// TestLocationRepository_FindVisibleByID_CrossHouseholdRejected proves a
// household B viewer cannot read household A's location by its real id: the
// bug class this catches is householdWhere silently dropping out of
// FindVisibleByID's WHERE clause, which would let any household enumerate
// every other household's location ids/names by brute-force id guessing.
func TestLocationRepository_FindVisibleByID_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)

	viewerB := identity.NewUserPrincipal(f.memberB, identity.RoleAdult, "Household B Viewer")
	viewerB.HouseholdID = f.householdB

	_, err := f.locations.FindVisibleByID(testCtx(t), viewerB, locA)
	if !errors.Is(err, domain.ErrLocationNotFound) {
		t.Errorf("FindVisibleByID(household B viewer, household A's location) = %v, want ErrLocationNotFound", err)
	}
}

// TestLocationRepository_List_ExcludesOtherHousehold proves List's own
// household predicate: household B has its own location seeded too, so the
// list is non-trivially non-empty, and household A's location must still be
// absent from it — the bug class an unqualified `SELECT * FROM location`
// (no WHERE at all) would produce.
func TestLocationRepository_List_ExcludesOtherHousehold(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	f.seedLocation(t, f.householdB, f.memberB)

	viewerB := identity.NewUserPrincipal(f.memberB, identity.RoleAdult, "Household B Viewer")
	viewerB.HouseholdID = f.householdB

	got, err := f.locations.List(testCtx(t), viewerB)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("List(household B viewer) must include household B's own location, want a non-trivially-empty result")
	}
	for _, l := range got {
		if l.ID == locA {
			t.Error("List(household B viewer) must not include household A's location")
		}
	}
}

// TestLocationRepository_Rename_CrossHouseholdRejected proves Rename's own
// household predicate, and — since a rejected UPDATE could still look like
// it "worked" if the assertion below were reading back with the wrong
// household's viewer — confirms household A's own name is unchanged by
// reading it back with household A's own viewer, per the ticket's own
// warning about a false-negative check.
func TestLocationRepository_Rename_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)

	viewerB := identity.NewUserPrincipal(f.memberB, identity.RoleAdult, "Household B Viewer")
	viewerB.HouseholdID = f.householdB

	err := f.locations.Rename(testCtx(t), viewerB, locA, "Hijacked")
	if !errors.Is(err, domain.ErrLocationNotFound) {
		t.Errorf("Rename(household B viewer, household A's location) = %v, want ErrLocationNotFound", err)
	}

	viewerA := identity.NewUserPrincipal(f.memberA, identity.RoleAdult, "Household A Viewer")
	viewerA.HouseholdID = f.householdA
	got, err := f.locations.FindVisibleByID(testCtx(t), viewerA, locA)
	if err != nil {
		t.Fatalf("FindVisibleByID(household A viewer) after rejected cross-household rename: %v", err)
	}
	if got.Name != "Garage" {
		t.Errorf("household A's location name = %q after a rejected cross-household Rename, want unchanged %q", got.Name, "Garage")
	}
}

// TestLocationRepository_Delete_CrossHouseholdRejected proves Delete's own
// household predicate, confirming (with household A's own viewer, not
// household B's) that the location row survives the rejected attempt.
func TestLocationRepository_Delete_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)

	viewerB := identity.NewUserPrincipal(f.memberB, identity.RoleAdult, "Household B Viewer")
	viewerB.HouseholdID = f.householdB

	err := f.locations.Delete(testCtx(t), viewerB, locA)
	if !errors.Is(err, domain.ErrLocationNotFound) {
		t.Errorf("Delete(household B viewer, household A's location) = %v, want ErrLocationNotFound", err)
	}

	viewerA := identity.NewUserPrincipal(f.memberA, identity.RoleAdult, "Household A Viewer")
	viewerA.HouseholdID = f.householdA
	if _, err := f.locations.FindVisibleByID(testCtx(t), viewerA, locA); err != nil {
		t.Errorf("FindVisibleByID(household A viewer) after rejected cross-household delete = %v, want nil (the location must survive)", err)
	}
}

// TestBinRepository_FindVisibleByID_CrossHouseholdRejected proves household
// B cannot read household A's bin by its real id, even though the bin is
// public (visibilityWhere alone would let it through — this is what proves
// householdWhere is a SEPARATE, AND-ed guard, not a replacement for it).
func TestBinRepository_FindVisibleByID_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHB-FIND")

	viewerB := identity.NewUserPrincipal(f.memberB, identity.RoleAdult, "Household B Viewer")
	viewerB.HouseholdID = f.householdB

	_, err := f.bins.FindVisibleByID(testCtx(t), viewerB, binA)
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("FindVisibleByID(household B viewer, household A's bin) = %v, want ErrBinNotFound", err)
	}
}

// TestBinRepository_ListVisible_ExcludesOtherHousehold proves ListVisible's
// own household predicate: household B's own bin keeps the list
// non-trivially non-empty, and household A's public bin must still be
// absent.
func TestBinRepository_ListVisible_ExcludesOtherHousehold(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHB-LISTV-A")
	locB := f.seedLocation(t, f.householdB, f.memberB)
	f.seedBin(t, f.householdB, locB, f.memberB, "XHHB-LISTV-B")

	viewerB := identity.NewUserPrincipal(f.memberB, identity.RoleAdult, "Household B Viewer")
	viewerB.HouseholdID = f.householdB

	got, err := f.bins.ListVisible(testCtx(t), viewerB)
	if err != nil {
		t.Fatalf("ListVisible: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("ListVisible(household B viewer) must include household B's own bin, want a non-trivially-empty result")
	}
	for _, b := range got {
		if b.ID == binA {
			t.Error("ListVisible(household B viewer) must not include household A's bin")
		}
	}
}

// TestBinRepository_ListVisibleByLocation_CrossHouseholdLocationEmpty proves
// ListVisibleByLocation returns empty for household A's own location id, not
// merely for household A's bins within it — the location id itself must not
// leak as a side channel (a non-empty result here, even with no bins in it,
// would confirm the location exists to a viewer who has no business knowing
// that).
func TestBinRepository_ListVisibleByLocation_CrossHouseholdLocationEmpty(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	f.seedBin(t, f.householdA, locA, f.memberA, "XHHB-LISTLOC")

	viewerB := identity.NewUserPrincipal(f.memberB, identity.RoleAdult, "Household B Viewer")
	viewerB.HouseholdID = f.householdB

	got, err := f.bins.ListVisibleByLocation(testCtx(t), viewerB, locA)
	if err != nil {
		t.Fatalf("ListVisibleByLocation: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListVisibleByLocation(household B viewer, household A's location) = %+v, want empty", got)
	}
}

// TestBinRepository_Update_CrossHouseholdRejected proves Update's own
// household predicate rejects a *domain.Bin naming household A's real bin
// id, even though the bin is public (so visibilityWhere alone would permit
// the mutation) — confirming with household A's own viewer that name is
// unchanged.
func TestBinRepository_Update_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHB-UPD")

	viewerB := identity.NewUserPrincipal(f.memberB, identity.RoleAdult, "Household B Viewer")
	viewerB.HouseholdID = f.householdB

	update := &domain.Bin{ID: binA, Name: "Hijacked", Visibility: domain.VisibilityPublic}
	err := f.bins.Update(testCtx(t), viewerB, update)
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("Update(household B viewer, household A's bin) = %v, want ErrBinNotFound", err)
	}

	viewerA := identity.NewUserPrincipal(f.memberA, identity.RoleAdult, "Household A Viewer")
	viewerA.HouseholdID = f.householdA
	got, err := f.bins.FindVisibleByID(testCtx(t), viewerA, binA)
	if err != nil {
		t.Fatalf("FindVisibleByID(household A viewer) after rejected cross-household update: %v", err)
	}
	if got.Name != "Bin XHHB-UPD" {
		t.Errorf("household A's bin name = %q after a rejected cross-household Update, want unchanged %q", got.Name, "Bin XHHB-UPD")
	}
}

// TestBinRepository_UpdateVisibility_CrossHouseholdRejected mirrors Update's
// own test for the narrower UpdateVisibility method.
func TestBinRepository_UpdateVisibility_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHB-UPDVIS")

	viewerB := identity.NewUserPrincipal(f.memberB, identity.RoleAdult, "Household B Viewer")
	viewerB.HouseholdID = f.householdB

	err := f.bins.UpdateVisibility(testCtx(t), viewerB, binA, domain.VisibilityPrivate)
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("UpdateVisibility(household B viewer, household A's bin) = %v, want ErrBinNotFound", err)
	}

	viewerA := identity.NewUserPrincipal(f.memberA, identity.RoleAdult, "Household A Viewer")
	viewerA.HouseholdID = f.householdA
	got, err := f.bins.FindVisibleByID(testCtx(t), viewerA, binA)
	if err != nil {
		t.Fatalf("FindVisibleByID(household A viewer) after rejected cross-household visibility update: %v", err)
	}
	if got.Visibility != domain.VisibilityPublic {
		t.Errorf("household A's bin visibility = %q after a rejected cross-household UpdateVisibility, want unchanged %q", got.Visibility, domain.VisibilityPublic)
	}
}

// TestBinRepository_Delete_CrossHouseholdRejected proves Delete's own
// household predicate, confirming (with household A's own viewer) that the
// bin survives.
func TestBinRepository_Delete_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHB-DEL")

	viewerB := identity.NewUserPrincipal(f.memberB, identity.RoleAdult, "Household B Viewer")
	viewerB.HouseholdID = f.householdB

	err := f.bins.Delete(testCtx(t), viewerB, binA)
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("Delete(household B viewer, household A's bin) = %v, want ErrBinNotFound", err)
	}

	viewerA := identity.NewUserPrincipal(f.memberA, identity.RoleAdult, "Household A Viewer")
	viewerA.HouseholdID = f.householdA
	if _, err := f.bins.FindVisibleByID(testCtx(t), viewerA, binA); err != nil {
		t.Errorf("FindVisibleByID(household A viewer) after rejected cross-household delete = %v, want nil (the bin must survive)", err)
	}
}

// TestBinRepository_GetForUpdate_CrossHouseholdRejected proves GetForUpdate
// rejects household B's own householdID against household A's real bin id.
// Called directly against f.pool (no explicit transaction): a bare SELECT
// ... FOR UPDATE outside a BEGIN still executes fine as an implicit
// single-statement transaction, so the caller-supplies-a-tx contract
// GetForUpdate's own doc describes for row-locking purposes is irrelevant to
// this scoping proof.
func TestBinRepository_GetForUpdate_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHB-GFU")

	_, err := f.bins.GetForUpdate(testCtx(t), f.householdB, binA)
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("GetForUpdate(household B, household A's bin) = %v, want ErrBinNotFound", err)
	}
}

// TestBinRepository_Move_CrossHouseholdRejected proves Move rejects
// household B's own householdID against household A's real bin id, even
// when target names a real, valid location household A itself owns —
// confirming (with household A's own viewer) that the bin's location is
// unchanged.
func TestBinRepository_Move_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA1 := f.seedLocation(t, f.householdA, f.memberA)
	locA2 := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA1, f.memberA, "XHHB-MOVE")

	_, err := f.bins.Move(testCtx(t), f.householdB, binA, locA2, time.Now())
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("Move(household B, household A's bin) = %v, want ErrBinNotFound", err)
	}

	viewerA := identity.NewUserPrincipal(f.memberA, identity.RoleAdult, "Household A Viewer")
	viewerA.HouseholdID = f.householdA
	got, err := f.bins.FindVisibleByID(testCtx(t), viewerA, binA)
	if err != nil {
		t.Fatalf("FindVisibleByID(household A viewer) after rejected cross-household move: %v", err)
	}
	if got.LocationID != locA1 {
		t.Errorf("household A's bin location = %v after a rejected cross-household Move, want unchanged %v", got.LocationID, locA1)
	}
}

// TestItemRepository_Get_CrossHouseholdRejected proves Get rejects household
// B's viewer against household A's real item id, even though the item sits
// in a public bin.
func TestItemRepository_Get_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHI-GET")
	itemA := f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")

	viewerB := identity.NewUserPrincipal(f.memberB, identity.RoleAdult, "Household B Viewer")
	viewerB.HouseholdID = f.householdB

	_, err := f.items.Get(testCtx(t), viewerB, itemA)
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Get(household B viewer, household A's item) = %v, want ErrItemNotFound", err)
	}
}

// TestItemRepository_GetForUpdate_CrossHouseholdRejected proves GetForUpdate
// rejects household B's own householdID against household A's real item id,
// mirroring TestBinRepository_GetForUpdate_CrossHouseholdRejected's own doc
// for why no explicit transaction is needed here either.
func TestItemRepository_GetForUpdate_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHI-GFU")
	itemA := f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")

	_, err := f.items.GetForUpdate(testCtx(t), f.householdB, itemA)
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("GetForUpdate(household B, household A's item) = %v, want ErrItemNotFound", err)
	}
}

// TestItemRepository_Update_CrossHouseholdRejected proves Update rejects
// household B's own householdID against a *domain.Item naming household A's
// real item id, confirming (with household A's own viewer) that name and
// quantity are unchanged.
func TestItemRepository_Update_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHI-UPD")
	itemA := f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")

	update := &domain.Item{ID: itemA, Name: "Hijacked", Quantity: 99}
	err := f.items.Update(testCtx(t), f.householdB, update)
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Update(household B, household A's item) = %v, want ErrItemNotFound", err)
	}

	viewerA := identity.NewUserPrincipal(f.memberA, identity.RoleAdult, "Household A Viewer")
	viewerA.HouseholdID = f.householdA
	got, err := f.items.Get(testCtx(t), viewerA, itemA)
	if err != nil {
		t.Fatalf("Get(household A viewer) after rejected cross-household update: %v", err)
	}
	if got.Name != "Household A Item" || got.Quantity != 1 {
		t.Errorf("household A's item = %+v after a rejected cross-household Update, want unchanged", got)
	}
}

// TestItemRepository_Move_CrossHouseholdRejected proves Move rejects
// household B's own householdID against household A's real item id, even
// when dst names household B's own real member (a placement that would
// otherwise be perfectly valid) — the household predicate must reject the
// row before the placement is ever considered, confirmed (with household
// A's own viewer) that the item's placement is unchanged.
func TestItemRepository_Move_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHI-MOVE")
	itemA := f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")

	_, err := f.items.Move(testCtx(t), f.householdB, itemA, domain.PlacementHeldBy(f.memberB))
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Move(household B, household A's item) = %v, want ErrItemNotFound", err)
	}

	viewerA := identity.NewUserPrincipal(f.memberA, identity.RoleAdult, "Household A Viewer")
	viewerA.HouseholdID = f.householdA
	got, err := f.items.Get(testCtx(t), viewerA, itemA)
	if err != nil {
		t.Fatalf("Get(household A viewer) after rejected cross-household move: %v", err)
	}
	if got.CurrentBinID == nil || *got.CurrentBinID != binA || got.HeldBy != nil {
		t.Errorf("household A's item placement = %+v after a rejected cross-household Move, want unchanged (still in binA)", got)
	}
}

// TestItemRepository_ListByBin_CrossHouseholdBinEmpty proves ListByBin
// returns empty for household A's own bin id, even though the bin holds a
// real (public) item.
func TestItemRepository_ListByBin_CrossHouseholdBinEmpty(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHI-LBB")
	f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")

	viewerB := identity.NewUserPrincipal(f.memberB, identity.RoleAdult, "Household B Viewer")
	viewerB.HouseholdID = f.householdB

	got, err := f.items.ListByBin(testCtx(t), viewerB, binA)
	if err != nil {
		t.Fatalf("ListByBin: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByBin(household B viewer, household A's bin) = %+v, want empty", got)
	}
}

// TestItemRepository_ListVisible_ExcludesOtherHousehold proves ListVisible's
// own household predicate with an empty domain.ItemFilter{}: household B's
// own item keeps the result non-trivially non-empty, and household A's item
// must still be absent.
func TestItemRepository_ListVisible_ExcludesOtherHousehold(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHI-LV-A")
	itemA := f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")

	locB := f.seedLocation(t, f.householdB, f.memberB)
	binB := f.seedBin(t, f.householdB, locB, f.memberB, "XHHI-LV-B")
	f.seedItem(t, f.householdB, binB, f.memberB, "Household B Item")

	viewerB := identity.NewUserPrincipal(f.memberB, identity.RoleAdult, "Household B Viewer")
	viewerB.HouseholdID = f.householdB

	got, err := f.items.ListVisible(testCtx(t), viewerB, domain.ItemFilter{})
	if err != nil {
		t.Fatalf("ListVisible: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("ListVisible(household B viewer) must include household B's own item, want a non-trivially-empty result")
	}
	for _, it := range got {
		if it.ID == itemA {
			t.Error("ListVisible(household B viewer) must not include household A's item")
		}
	}
}

// TestItemRepository_CountsByBin_ExcludesOtherHouseholdBin proves
// CountsByBin's own household predicate: household A's bin id must not
// appear as a key at all in household B's own counts map — the INNER JOIN's
// "absent means zero" contract (see CountsByBin's own doc), not present with
// a value of 0.
func TestItemRepository_CountsByBin_ExcludesOtherHouseholdBin(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHI-CBB")
	f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")

	viewerB := identity.NewUserPrincipal(f.memberB, identity.RoleAdult, "Household B Viewer")
	viewerB.HouseholdID = f.householdB

	counts, err := f.items.CountsByBin(testCtx(t), viewerB)
	if err != nil {
		t.Fatalf("CountsByBin: %v", err)
	}
	if _, ok := counts[binA]; ok {
		t.Errorf("CountsByBin(household B viewer) = %v, must not carry a key for household A's bin at all", counts)
	}
}

// TestItemRepository_Delete_CrossHouseholdRejected proves Delete's own
// household predicate, confirming (with household A's own viewer) that the
// item survives.
func TestItemRepository_Delete_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHI-DEL")
	itemA := f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")

	err := f.items.Delete(testCtx(t), f.householdB, itemA)
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Delete(household B, household A's item) = %v, want ErrItemNotFound", err)
	}

	viewerA := identity.NewUserPrincipal(f.memberA, identity.RoleAdult, "Household A Viewer")
	viewerA.HouseholdID = f.householdA
	if _, err := f.items.Get(testCtx(t), viewerA, itemA); err != nil {
		t.Errorf("Get(household A viewer) after rejected cross-household delete = %v, want nil (the item must survive)", err)
	}
}

// TestItemRepository_FindVisibleDetail_CrossHouseholdRejected proves
// FindVisibleDetail rejects household B's viewer against household A's real
// item id — the read model NSTR-42's item detail page renders, so a leak
// here would surface a stranger's bin/location/holder names, not merely an
// id.
func TestItemRepository_FindVisibleDetail_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHI-DETAIL")
	itemA := f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")

	viewerB := identity.NewUserPrincipal(f.memberB, identity.RoleAdult, "Household B Viewer")
	viewerB.HouseholdID = f.householdB

	_, err := f.items.FindVisibleDetail(testCtx(t), viewerB, itemA)
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("FindVisibleDetail(household B viewer, household A's item) = %v, want ErrItemNotFound", err)
	}
}

// TestItemRepository_SearchVisible_ExcludesOtherHousehold proves
// SearchVisible cannot be used to enumerate another household's item names:
// household A's item carries a distinctive, guaranteed-unique name
// ("Xylophone9271"), and searching for a substring of it as household B must
// return no results — an item_name_trgm-accelerated leak would otherwise let
// any household fish for another's inventory by guessing common words.
func TestItemRepository_SearchVisible_ExcludesOtherHousehold(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHI-SEARCH")
	f.seedItem(t, f.householdA, binA, f.memberA, "Xylophone9271")

	viewerB := identity.NewUserPrincipal(f.memberB, identity.RoleAdult, "Household B Viewer")
	viewerB.HouseholdID = f.householdB

	got, err := f.items.SearchVisible(testCtx(t), viewerB, "Xylophone9271", 10)
	if err != nil {
		t.Fatalf("SearchVisible: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("SearchVisible(household B viewer, household A's item name) = %+v, want empty", got)
	}
}

// TestItemRepository_ListIDsByBin_CrossHouseholdBinEmpty proves ListIDsByBin
// returns empty for household A's own bin id — the unscoped-by-visibility
// audit read NSTR-41's BinMover fans EventMoved rows out from, so a leak
// here would let household B's own bin-move fan out household A's item
// names via a mismatched householdID somehow reaching this call.
func TestItemRepository_ListIDsByBin_CrossHouseholdBinEmpty(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHI-IDSBIN")
	f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")

	got, err := f.items.ListIDsByBin(testCtx(t), f.householdB, binA)
	if err != nil {
		t.Fatalf("ListIDsByBin: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListIDsByBin(household B, household A's bin) = %+v, want empty", got)
	}
}

// TestItemLinkRepository_Create_CrossHouseholdItemRejected proves Create
// rejects household B's own householdID against a *domain.ItemLink naming
// household A's real item id. item_link_postgres.go's Create implementation
// is an INSERT ... SELECT ... WHERE EXISTS, whose failure surfaces as
// pgx.ErrNoRows rather than a foreign-key violation — confirmed by reading
// Create's actual body rather than assumed — which the adapter already maps
// to domain.ErrItemNotFound; this test additionally confirms no orphan link
// row was inserted.
func TestItemLinkRepository_Create_CrossHouseholdItemRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHL-CREATE")
	itemA := f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")

	link := &domain.ItemLink{ID: domain.NewItemLinkID(), ItemID: itemA, Label: "Hijacked", URL: "https://evil.example.com", Position: 0}
	err := f.links.Create(testCtx(t), f.householdB, link)
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Create(household B, household A's item) = %v, want ErrItemNotFound", err)
	}

	got, err := f.links.ListByItem(testCtx(t), f.householdA, itemA)
	if err != nil {
		t.Fatalf("ListByItem(household A) after rejected cross-household create: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByItem(household A) after a rejected cross-household Create = %+v, want no orphan link inserted", got)
	}
}

// TestItemLinkRepository_Update_CrossHouseholdRejected proves Update rejects
// household B's own householdID against household A's real item id and link
// id together, confirming (via household A's own householdID) the link's
// label is unchanged.
func TestItemLinkRepository_Update_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHL-UPD")
	itemA := f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")
	linkA := f.seedItemLink(t, f.householdA, itemA)

	err := f.links.Update(testCtx(t), f.householdB, itemA, linkA, "Hijacked", "https://evil.example.com")
	if !errors.Is(err, domain.ErrItemLinkNotFound) {
		t.Errorf("Update(household B, household A's link) = %v, want ErrItemLinkNotFound", err)
	}

	got, err := f.links.ListByItem(testCtx(t), f.householdA, itemA)
	if err != nil {
		t.Fatalf("ListByItem(household A) after rejected cross-household update: %v", err)
	}
	if len(got) != 1 || got[0].Label != "Manual" {
		t.Errorf("household A's link = %+v after a rejected cross-household Update, want unchanged (label %q)", got, "Manual")
	}
}

// TestItemLinkRepository_Delete_CrossHouseholdRejected proves Delete rejects
// household B's own householdID against household A's real item id and link
// id together, confirming the link row survives.
func TestItemLinkRepository_Delete_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHL-DEL")
	itemA := f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")
	linkA := f.seedItemLink(t, f.householdA, itemA)

	err := f.links.Delete(testCtx(t), f.householdB, itemA, linkA)
	if !errors.Is(err, domain.ErrItemLinkNotFound) {
		t.Errorf("Delete(household B, household A's link) = %v, want ErrItemLinkNotFound", err)
	}

	got, err := f.links.ListByItem(testCtx(t), f.householdA, itemA)
	if err != nil {
		t.Fatalf("ListByItem(household A) after rejected cross-household delete: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("ListByItem(household A) after a rejected cross-household Delete = %+v, want the link still present", got)
	}
}

// TestItemLinkRepository_ListByItem_CrossHouseholdEmpty proves ListByItem
// returns empty for household A's own item id, even though it has a real
// link.
func TestItemLinkRepository_ListByItem_CrossHouseholdEmpty(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHL-LIST")
	itemA := f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")
	f.seedItemLink(t, f.householdA, itemA)

	got, err := f.links.ListByItem(testCtx(t), f.householdB, itemA)
	if err != nil {
		t.Fatalf("ListByItem(household B, household A's item): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByItem(household B, household A's item) = %+v, want empty", got)
	}
}

// TestItemLinkRepository_NextPosition_CrossHouseholdZero proves NextPosition
// returns 0 for household A's own item id even though it already has a link
// at position 0: household B's view of "how many links does this item
// have" must read 0, not household A's real count of 1 — the bug class a
// missing join-side household predicate would produce (a household B caller
// computing a colliding next position against another household's item).
func TestItemLinkRepository_NextPosition_CrossHouseholdZero(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHL-NEXTPOS")
	itemA := f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")
	f.seedItemLink(t, f.householdA, itemA)

	next, err := f.links.NextPosition(testCtx(t), f.householdB, itemA)
	if err != nil {
		t.Fatalf("NextPosition(household B, household A's item): %v", err)
	}
	if next != 0 {
		t.Errorf("NextPosition(household B, household A's item) = %d, want 0", next)
	}
}

// TestItemEventRepository_ListByItem_CrossHouseholdEmpty proves ListByItem
// returns empty for household A's own item id, even though it has a real
// event — the item timeline the AC calls out by name.
func TestItemEventRepository_ListByItem_CrossHouseholdEmpty(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHE-LISTITEM")
	itemA := f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")
	f.seedItemEvent(t, f.householdA, f.memberA, itemA, "Household A Item")

	got, err := f.events.ListByItem(testCtx(t), f.householdB, itemA, domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByItem(household B, household A's item): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByItem(household B, household A's item) = %+v, want empty", got)
	}
}

// TestItemEventRepository_ListByBin_CrossHouseholdEmpty proves ListByBin
// returns empty for household A's own bin id, even though it has a real
// event naming that bin (seeded via seedBinItemEvent, an EventAdded row —
// see that helper's own doc for why EventCreated would not exercise this
// path).
func TestItemEventRepository_ListByBin_CrossHouseholdEmpty(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHE-LISTBIN")
	itemA := f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")
	f.seedBinItemEvent(t, f.householdA, f.memberA, itemA, "Household A Item", binA)

	got, err := f.events.ListByBin(testCtx(t), f.householdB, binA, domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByBin(household B, household A's bin): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByBin(household B, household A's bin) = %+v, want empty", got)
	}
}

// TestReturnRequestRepository_ListByItem_CrossHouseholdEmpty proves
// ListByItem returns empty for household A's own item id, even though it has
// a real open request.
func TestReturnRequestRepository_ListByItem_CrossHouseholdEmpty(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHR-LIST")
	itemA := f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")
	holderA := f.seedMember(t, f.householdA)
	f.seedReturnRequest(t, f.householdA, itemA, f.memberA, holderA)

	got, err := f.requests.ListByItem(testCtx(t), f.householdB, itemA)
	if err != nil {
		t.Fatalf("ListByItem(household B, household A's item): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByItem(household B, household A's item) = %+v, want empty", got)
	}
}

// TestReturnRequestRepository_Cancel_CrossHouseholdRejected proves Cancel
// rejects household B's own householdID against household A's real request
// id AND household A's real requesterID together — the household predicate
// must reject even though requesterID matches exactly, confirming (via
// household A's own ListByItem) the request is still open afterward.
func TestReturnRequestRepository_Cancel_CrossHouseholdRejected(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHR-CANCEL")
	itemA := f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")
	holderA := f.seedMember(t, f.householdA)
	requestA := f.seedReturnRequest(t, f.householdA, itemA, f.memberA, holderA)

	_, err := f.requests.Cancel(testCtx(t), f.householdB, requestA, f.memberA)
	if !errors.Is(err, domain.ErrReturnRequestNotFound) {
		t.Errorf("Cancel(household B, household A's request) = %v, want ErrReturnRequestNotFound", err)
	}

	got, err := f.requests.ListByItem(testCtx(t), f.householdA, itemA)
	if err != nil {
		t.Fatalf("ListByItem(household A) after rejected cross-household cancel: %v", err)
	}
	if len(got) != 1 || got[0].Status != domain.ReturnRequestStatusOpen {
		t.Errorf("household A's request = %+v after a rejected cross-household Cancel, want still open", got)
	}
}

// TestReturnRequestRepository_FulfillOpenForItem_CrossHouseholdNoop proves
// FulfillOpenForItem returns an empty slice for household A's own item id
// under household B's own householdID, confirming (via household A's own
// ListByItem) the real request is still open — the household predicate must
// stop a household B caller from silently resolving a stranger's return
// request as a side effect of its own (unrelated) item id happening to
// collide with a valid one elsewhere.
func TestReturnRequestRepository_FulfillOpenForItem_CrossHouseholdNoop(t *testing.T) {
	f := newHouseholdScopingFixture(t)
	locA := f.seedLocation(t, f.householdA, f.memberA)
	binA := f.seedBin(t, f.householdA, locA, f.memberA, "XHHR-FULFILL")
	itemA := f.seedItem(t, f.householdA, binA, f.memberA, "Household A Item")
	holderA := f.seedMember(t, f.householdA)
	f.seedReturnRequest(t, f.householdA, itemA, f.memberA, holderA)

	fulfilled, err := f.requests.FulfillOpenForItem(testCtx(t), f.householdB, itemA, time.Now())
	if err != nil {
		t.Fatalf("FulfillOpenForItem(household B, household A's item): %v", err)
	}
	if len(fulfilled) != 0 {
		t.Errorf("FulfillOpenForItem(household B, household A's item) = %+v, want empty", fulfilled)
	}

	got, err := f.requests.ListByItem(testCtx(t), f.householdA, itemA)
	if err != nil {
		t.Fatalf("ListByItem(household A) after cross-household FulfillOpenForItem: %v", err)
	}
	if len(got) != 1 || got[0].Status != domain.ReturnRequestStatusOpen {
		t.Errorf("household A's request = %+v after a cross-household FulfillOpenForItem, want still open", got)
	}
}
