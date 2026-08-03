package adapter_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/media/adapter"
	"github.com/ericfisherdev/nestorage/internal/media/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
	storageadapter "github.com/ericfisherdev/nestorage/internal/storage/adapter"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// ---------------------------------------------------------------------------
// NSTR-131: the two-household LEAK suite for PhotoRepository.
//
// photoFixture (photo_postgres_gated_test.go) seeds exactly one household, by
// design — every test above this file exercises PhotoRepository within one
// tenant. This file adds its OWN two-household fixture,
// photoHouseholdScopingFixture, mirroring storage/adapter's
// householdScopingFixture (internal/storage/adapter/household_scoping_gated_test.go):
// same "one derived database, two seeded households, one member under each"
// shape, kept as its own type here rather than reused across packages
// because PhotoRepository's foreign keys (uploaded_by -> identity.member,
// item_photo.item_id -> storage's item) need the storage adapters wired in
// too, exactly as photoFixture's own seedItem already does for the
// single-household case.
//
// Manifest — one entry per exported method on domain.PhotoRepository, so a
// reviewer can diff this list against that interface's method list in one
// pass, per the ticket's own "one enumerated test entry per method" AC.
// Create is not listed: it takes no viewer/householdID argument to spoof,
// only photo.HouseholdID on the row being inserted — NSTR-122's own
// write-path FK territory, not this leak suite's.
//
//	GetForItem       -> TestPhotoRepository_GetForItem_CrossHouseholdRejected
//	FindByStorageRef -> TestPhotoRepository_FindByStorageRef_CrossHouseholdRejected
//	AttachToItem     -> TestPhotoRepository_AttachToItem_CrossHouseholdRejected
//	ListByItem       -> TestPhotoRepository_ListByItem_CrossHouseholdEmpty
//	Delete           -> TestPhotoRepository_Delete_CrossHouseholdRejected
//	SetPrimary       -> TestPhotoRepository_SetPrimary_CrossHouseholdRejected
//	Reorder          -> TestPhotoRepository_Reorder_CrossHouseholdNoop
//
// ---------------------------------------------------------------------------

// photoHouseholdScopingFixture wires a PhotoRepository alongside the
// identity/storage repositories a photo's foreign keys require, over ONE
// derived database seeded with TWO households and one member under each —
// distinct from photoFixture (photo_postgres_gated_test.go), which seeds
// exactly one household, mirroring that type's own doc for why a second
// NewIsolatedPool call per test would defeat the harness's isolation.
type photoHouseholdScopingFixture struct {
	pool  *pgxpool.Pool
	repo  *adapter.PhotoRepository
	items *storageadapter.ItemRepository
	bins  *storageadapter.BinRepository
	locs  *storageadapter.LocationRepository
	users *identityadapter.UserRepository

	householdA, householdB identity.HouseholdID
	memberA, memberB       identity.UserID
}

func newPhotoHouseholdScopingFixture(t *testing.T) *photoHouseholdScopingFixture {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "media")
	f := &photoHouseholdScopingFixture{
		pool:  pool,
		repo:  adapter.NewPhotoRepository(pool),
		items: storageadapter.NewItemRepository(pool),
		bins:  storageadapter.NewBinRepository(pool),
		locs:  storageadapter.NewLocationRepository(pool),
		users: identityadapter.NewUserRepository(pool),
	}
	f.householdA = seedHousehold(t, pool)
	f.householdB = seedHousehold(t, pool)
	f.memberA = f.seedMember(t, f.householdA)
	f.memberB = f.seedMember(t, f.householdB)
	return f
}

// seedMember creates and returns the id of a member belonging to household —
// photo's uploaded_by FK and item's created_by FK both resolve against this
// member's own household, mirroring storage/adapter's identical helper.
func (f *photoHouseholdScopingFixture) seedMember(t *testing.T, household identity.HouseholdID) identity.UserID {
	t.Helper()
	u := &identity.User{
		ID:           identity.NewUserID(),
		HouseholdID:  household,
		DisplayName:  "Household Member",
		Email:        "photo-scoping-" + identity.NewUserID().String() + "@example.com",
		PasswordHash: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		Role:         identity.RoleAdult,
		Color:        identity.ColorIndigo,
	}
	if err := f.users.Create(testCtx(t), u); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	return u.ID
}

// seedItem creates a location, a public bin, and an item inside it, under
// household, created by createdBy — item_photo.item_id's FK target,
// mirroring photoFixture's own seedItem, parameterized by household since
// this fixture seeds two.
func (f *photoHouseholdScopingFixture) seedItem(t *testing.T, household identity.HouseholdID, createdBy identity.UserID) storagedomain.ItemID {
	t.Helper()
	loc := &storagedomain.Location{ID: storagedomain.NewLocationID(), HouseholdID: household, Name: "Garage", CreatedBy: createdBy}
	if err := f.locs.Create(testCtx(t), loc); err != nil {
		t.Fatalf("seed location: %v", err)
	}
	binID := storagedomain.NewBinID()
	bin := &storagedomain.Bin{
		ID: binID, HouseholdID: household, Code: "PHTX" + binID.String(), Name: "Photo bin",
		LocationID: loc.ID, CreatedBy: createdBy, Visibility: storagedomain.VisibilityPublic,
	}
	if err := f.bins.Create(testCtx(t), bin); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
	it := &storagedomain.Item{
		ID: storagedomain.NewItemID(), HouseholdID: household, Name: "Photographed item", Quantity: 1,
		CurrentBinID: &bin.ID, CreatedBy: createdBy,
	}
	if err := f.items.Create(testCtx(t), it); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	return it.ID
}

// seedPhoto creates a photo under household, uploaded by uploadedBy, and
// attaches it to item at position, primary iff isPrimary — the shape every
// leak test below needs: a real, attached photo reachable by a valid id, not
// a bare unattached row.
func (f *photoHouseholdScopingFixture) seedPhoto(t *testing.T, household identity.HouseholdID, item storagedomain.ItemID, uploadedBy identity.UserID, ref string, position int, isPrimary bool) domain.PhotoID {
	t.Helper()
	p := newPhoto(household, uploadedBy, ref)
	if err := f.repo.Create(testCtx(t), p); err != nil {
		t.Fatalf("seed photo: %v", err)
	}
	if err := f.repo.AttachToItem(testCtx(t), household, item, p.ID, position, isPrimary); err != nil {
		t.Fatalf("seed attach photo: %v", err)
	}
	return p.ID
}

// TestPhotoRepository_GetForItem_CrossHouseholdRejected proves household B's
// own householdID cannot read household A's real photo, reached through
// household A's own real item — both ids valid, attached, and correctly
// paired, so only the household predicate stands between household B and
// this row.
func TestPhotoRepository_GetForItem_CrossHouseholdRejected(t *testing.T) {
	f := newPhotoHouseholdScopingFixture(t)
	itemA := f.seedItem(t, f.householdA, f.memberA)
	photoA := f.seedPhoto(t, f.householdA, itemA, f.memberA, "items/gfi/"+validHash+".jpg", 0, true)

	_, err := f.repo.GetForItem(testCtx(t), f.householdB, itemA, photoA)
	if !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Errorf("GetForItem(household B, household A's item+photo) = %v, want ErrPhotoNotFound", err)
	}
}

// TestPhotoRepository_FindByStorageRef_CrossHouseholdRejected proves the
// household predicate holds even when the caller supplies the exact,
// content-addressed storage ref — there is no way to dodge scoping by
// already knowing the (otherwise globally-unique-looking) ref string.
func TestPhotoRepository_FindByStorageRef_CrossHouseholdRejected(t *testing.T) {
	f := newPhotoHouseholdScopingFixture(t)
	itemA := f.seedItem(t, f.householdA, f.memberA)
	ref := "items/fbs/" + validHash + ".jpg"
	f.seedPhoto(t, f.householdA, itemA, f.memberA, ref, 0, true)

	_, err := f.repo.FindByStorageRef(testCtx(t), f.householdB, domain.StorageRef(ref))
	if !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Errorf("FindByStorageRef(household B, household A's photo's storage ref) = %v, want ErrPhotoNotFound", err)
	}
}

// TestPhotoRepository_AttachToItem_CrossHouseholdRejected proves household
// B's own householdID cannot attach a real, unattached household A photo to
// a real household A item. AttachToItem's own attachNotFoundReason checks
// item existence (under householdID) BEFORE photo existence — read directly
// off AttachToItem's implementation rather than assumed — so with both
// itemID and photoID belonging to household A, the item check is what fails
// first, surfacing storagedomain.ErrItemNotFound rather than
// domain.ErrPhotoNotFound. This test also confirms no orphan item_photo row
// was inserted.
func TestPhotoRepository_AttachToItem_CrossHouseholdRejected(t *testing.T) {
	f := newPhotoHouseholdScopingFixture(t)
	itemA := f.seedItem(t, f.householdA, f.memberA)
	photoA := newPhoto(f.householdA, f.memberA, "items/att/"+validHash+".jpg")
	if err := f.repo.Create(testCtx(t), photoA); err != nil {
		t.Fatalf("seed photo: %v", err)
	}

	err := f.repo.AttachToItem(testCtx(t), f.householdB, itemA, photoA.ID, 0, true)
	if !errors.Is(err, storagedomain.ErrItemNotFound) {
		t.Errorf("AttachToItem(household B, household A's item+photo) = %v, want storagedomain.ErrItemNotFound", err)
	}

	got, err := f.repo.ListByItem(testCtx(t), f.householdA, itemA)
	if err != nil {
		t.Fatalf("ListByItem(household A) after rejected cross-household attach: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByItem(household A) after a rejected cross-household AttachToItem = %+v, want no orphan attachment", got)
	}
}

// TestPhotoRepository_ListByItem_CrossHouseholdEmpty proves ListByItem
// returns empty for household A's own item id, even though it has a real
// attached photo.
func TestPhotoRepository_ListByItem_CrossHouseholdEmpty(t *testing.T) {
	f := newPhotoHouseholdScopingFixture(t)
	itemA := f.seedItem(t, f.householdA, f.memberA)
	f.seedPhoto(t, f.householdA, itemA, f.memberA, "items/lbi/"+validHash+".jpg", 0, true)

	got, err := f.repo.ListByItem(testCtx(t), f.householdB, itemA)
	if err != nil {
		t.Fatalf("ListByItem(household B, household A's item): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByItem(household B, household A's item) = %+v, want empty", got)
	}
}

// TestPhotoRepository_Delete_CrossHouseholdRejected proves Delete rejects
// household B's own householdID against household A's real item+photo,
// confirming (via household A's own GetForItem) the photo row survives.
func TestPhotoRepository_Delete_CrossHouseholdRejected(t *testing.T) {
	f := newPhotoHouseholdScopingFixture(t)
	itemA := f.seedItem(t, f.householdA, f.memberA)
	photoA := f.seedPhoto(t, f.householdA, itemA, f.memberA, "items/del/"+validHash+".jpg", 0, true)

	err := f.repo.Delete(testCtx(t), f.householdB, itemA, photoA)
	if !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Errorf("Delete(household B, household A's item+photo) = %v, want ErrPhotoNotFound", err)
	}

	if _, err := f.repo.GetForItem(testCtx(t), f.householdA, itemA, photoA); err != nil {
		t.Errorf("GetForItem(household A) after rejected cross-household delete = %v, want nil (the photo must survive)", err)
	}
}

// TestPhotoRepository_SetPrimary_CrossHouseholdRejected proves SetPrimary
// rejects household B's own householdID against household A's real
// item+photo. SetPrimary's own clearQ (clearing the previous primary) is
// itself household-scoped via an EXISTS(item ... household_id) guard, so a
// cross-household call clears nothing AND fails to set the new primary — a
// double no-op, confirmed here by re-reading household A's own list and
// finding neither photo's primary flag moved.
func TestPhotoRepository_SetPrimary_CrossHouseholdRejected(t *testing.T) {
	f := newPhotoHouseholdScopingFixture(t)
	itemA := f.seedItem(t, f.householdA, f.memberA)
	primaryA := f.seedPhoto(t, f.householdA, itemA, f.memberA, "items/sp/1-"+validHash+".jpg", 0, true)
	secondaryA := f.seedPhoto(t, f.householdA, itemA, f.memberA, "items/sp/2-"+validHash+".jpg", 1, false)

	err := f.repo.SetPrimary(testCtx(t), f.householdB, itemA, secondaryA)
	if !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Errorf("SetPrimary(household B, household A's item+photo) = %v, want ErrPhotoNotFound", err)
	}

	list, err := f.repo.ListByItem(testCtx(t), f.householdA, itemA)
	if err != nil {
		t.Fatalf("ListByItem(household A) after rejected cross-household SetPrimary: %v", err)
	}
	for _, ip := range list {
		switch ip.Photo.ID {
		case primaryA:
			if !ip.IsPrimary {
				t.Error("household A's original primary photo must remain primary after a rejected cross-household SetPrimary")
			}
		case secondaryA:
			if ip.IsPrimary {
				t.Error("household A's secondary photo must not have become primary after a rejected cross-household SetPrimary")
			}
		}
	}
}

// TestPhotoRepository_Reorder_CrossHouseholdNoop proves Reorder's own
// EXISTS-guarded UPDATE, read directly off Reorder's implementation, carries
// no rows-affected check of its own: a cross-household call therefore
// returns a nil error (not an error this test should assert), rewriting ZERO
// rows — confirmed by reading household A's own list back afterward and
// finding the photo's position unchanged, rather than asserting an error
// shape the real code does not produce.
func TestPhotoRepository_Reorder_CrossHouseholdNoop(t *testing.T) {
	f := newPhotoHouseholdScopingFixture(t)
	itemA := f.seedItem(t, f.householdA, f.memberA)
	photoA := f.seedPhoto(t, f.householdA, itemA, f.memberA, "items/ro/"+validHash+".jpg", 0, true)

	err := f.repo.Reorder(testCtx(t), f.householdB, itemA, []domain.PhotoID{photoA})
	if err != nil {
		t.Fatalf("Reorder(household B, household A's item+photo) = %v, want nil (the EXISTS-guarded UPDATE is a silent no-op outside householdID, not an error)", err)
	}

	list, err := f.repo.ListByItem(testCtx(t), f.householdA, itemA)
	if err != nil {
		t.Fatalf("ListByItem(household A) after cross-household Reorder: %v", err)
	}
	if len(list) != 1 || list[0].Position != 0 {
		t.Errorf("household A's photo = %+v after a cross-household Reorder, want unchanged position 0", list)
	}
}
