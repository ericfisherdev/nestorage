package adapter_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/media/adapter"
	"github.com/ericfisherdev/nestorage/internal/media/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
	storageadapter "github.com/ericfisherdev/nestorage/internal/storage/adapter"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// photoFixture wires a PhotoRepository alongside the identity/storage
// repositories a photo's foreign keys require (uploaded_by -> app_user,
// item_photo.item_id -> item), over ONE derived database — its own
// "media" suffix, since dbtest.Harness.NewIsolatedPool must be called
// exactly once per test (see storage's identical fixture doc).
type photoFixture struct {
	pool  *pgxpool.Pool
	repo  *adapter.PhotoRepository
	items *storageadapter.ItemRepository
	bins  *storageadapter.BinRepository
	locs  *storageadapter.LocationRepository
	users *identityadapter.UserRepository
}

func newPhotoFixture(t *testing.T) *photoFixture {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "media")
	return &photoFixture{
		pool:  pool,
		repo:  adapter.NewPhotoRepository(pool),
		items: storageadapter.NewItemRepository(pool),
		bins:  storageadapter.NewBinRepository(pool),
		locs:  storageadapter.NewLocationRepository(pool),
		users: identityadapter.NewUserRepository(pool),
	}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func (f *photoFixture) seedUser(t *testing.T) identity.UserID {
	t.Helper()
	u := &identity.User{
		ID:           identity.NewUserID(),
		DisplayName:  "Test User",
		Email:        "photo-user-" + identity.NewUserID().String() + "@example.com",
		PasswordHash: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		Role:         identity.RoleMember,
		Color:        identity.ColorIndigo,
	}
	if err := f.users.Create(testCtx(t), u); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u.ID
}

// seedItem creates a location, a public bin, and an item inside it,
// returning the item's id — item_photo.item_id's FK target.
func (f *photoFixture) seedItem(t *testing.T, createdBy identity.UserID) storagedomain.ItemID {
	t.Helper()
	loc := &storagedomain.Location{ID: storagedomain.NewLocationID(), Name: "Garage", CreatedBy: createdBy}
	if err := f.locs.Create(testCtx(t), loc); err != nil {
		t.Fatalf("seed location: %v", err)
	}
	binID := storagedomain.NewBinID()
	bin := &storagedomain.Bin{
		ID: binID, Code: "PHT" + binID.String(), Name: "Photo bin",
		LocationID: loc.ID, CreatedBy: createdBy, Visibility: storagedomain.VisibilityPublic,
	}
	if err := f.bins.Create(testCtx(t), bin); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
	it := &storagedomain.Item{
		ID: storagedomain.NewItemID(), Name: "Photographed item", Quantity: 1,
		CurrentBinID: &bin.ID, CreatedBy: createdBy,
	}
	if err := f.items.Create(testCtx(t), it); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	return it.ID
}

func newPhoto(uploadedBy identity.UserID, ref string) *domain.Photo {
	return &domain.Photo{
		ID: domain.NewPhotoID(), StorageRef: domain.StorageRef(ref),
		ContentHash: validHash, SizeBytes: 1024, ContentType: domain.ContentTypeJPEG,
		StorageBackend: domain.StorageBackendLocal, UploadedBy: uploadedBy,
	}
}

// validHash is a well-formed 64-character lowercase hex sha256 — the shape
// every stored photo's content_sha256 must have.
const validHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func TestPhotoRepository_CreateAndFindByStorageRef(t *testing.T) {
	f := newPhotoFixture(t)
	uploader := f.seedUser(t)
	p := newPhoto(uploader, "items/some-item/"+validHash+".jpg")

	if err := f.repo.Create(testCtx(t), p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.CreatedAt.IsZero() {
		t.Error("Create left CreatedAt zero")
	}

	got, err := f.repo.FindByStorageRef(testCtx(t), p.StorageRef)
	if err != nil {
		t.Fatalf("FindByStorageRef: %v", err)
	}
	if got.ID != p.ID || got.ContentHash != p.ContentHash || got.SizeBytes != p.SizeBytes || got.ContentType != p.ContentType {
		t.Errorf("FindByStorageRef = %+v, want it to match the created photo", got)
	}
	if got.StorageBackend != domain.StorageBackendLocal {
		t.Errorf("FindByStorageRef.StorageBackend = %q, want %q", got.StorageBackend, domain.StorageBackendLocal)
	}
}

func TestPhotoRepository_FindByStorageRef_NotFound(t *testing.T) {
	f := newPhotoFixture(t)
	_, err := f.repo.FindByStorageRef(testCtx(t), domain.StorageRef("items/nope/deadbeef.jpg"))
	if !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Errorf("FindByStorageRef(unknown) = %v, want ErrPhotoNotFound", err)
	}
}

func TestPhotoRepository_Create_DuplicateStorageRefRejected(t *testing.T) {
	f := newPhotoFixture(t)
	uploader := f.seedUser(t)
	ref := "items/some-item/" + validHash + ".jpg"

	if err := f.repo.Create(testCtx(t), newPhoto(uploader, ref)); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	err := f.repo.Create(testCtx(t), newPhoto(uploader, ref))
	if !errors.Is(err, domain.ErrDuplicatePhoto) {
		t.Errorf("Create(duplicate storage ref) = %v, want ErrDuplicatePhoto", err)
	}
}

func TestPhotoRepository_Create_UnknownUploaderRejected(t *testing.T) {
	f := newPhotoFixture(t)
	ghost := identity.NewUserID()
	err := f.repo.Create(testCtx(t), newPhoto(ghost, "items/some-item/"+validHash+".jpg"))
	if !errors.Is(err, identity.ErrUserNotFound) {
		t.Errorf("Create(unknown uploaded_by) = %v, want identity.ErrUserNotFound", err)
	}
}

func TestPhotoRepository_AttachToItemAndListByItemAndGetForItem(t *testing.T) {
	f := newPhotoFixture(t)
	uploader := f.seedUser(t)
	itemID := f.seedItem(t, uploader)
	p1 := newPhoto(uploader, "items/i/"+validHash+".jpg")
	p2 := newPhoto(uploader, "items/i/other-"+validHash+".jpg")
	for _, p := range []*domain.Photo{p1, p2} {
		if err := f.repo.Create(testCtx(t), p); err != nil {
			t.Fatalf("Create(%s): %v", p.StorageRef, err)
		}
	}

	if err := f.repo.AttachToItem(testCtx(t), itemID, p1.ID, 0, true); err != nil {
		t.Fatalf("AttachToItem(p1): %v", err)
	}
	if err := f.repo.AttachToItem(testCtx(t), itemID, p2.ID, 1, false); err != nil {
		t.Fatalf("AttachToItem(p2): %v", err)
	}

	list, err := f.repo.ListByItem(testCtx(t), itemID)
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListByItem returned %d photos, want 2", len(list))
	}
	if list[0].Photo.ID != p1.ID || !list[0].IsPrimary || list[0].Position != 0 {
		t.Errorf("ListByItem[0] = %+v, want p1 primary at position 0", list[0])
	}
	if list[1].Photo.ID != p2.ID || list[1].IsPrimary || list[1].Position != 1 {
		t.Errorf("ListByItem[1] = %+v, want p2 non-primary at position 1", list[1])
	}

	got, err := f.repo.GetForItem(testCtx(t), itemID, p1.ID)
	if err != nil {
		t.Fatalf("GetForItem: %v", err)
	}
	if got.ID != p1.ID {
		t.Errorf("GetForItem = %+v, want p1", got)
	}
}

func TestPhotoRepository_GetForItem_WrongItemNotFound(t *testing.T) {
	f := newPhotoFixture(t)
	uploader := f.seedUser(t)
	itemA := f.seedItem(t, uploader)
	itemB := f.seedItem(t, uploader)
	p := newPhoto(uploader, "items/a/"+validHash+".jpg")
	if err := f.repo.Create(testCtx(t), p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.repo.AttachToItem(testCtx(t), itemA, p.ID, 0, true); err != nil {
		t.Fatalf("AttachToItem: %v", err)
	}

	// A photo attached to itemA must not be reachable through itemB — the
	// visibility-scoping guard Sprint 5 reconciliation R5 requires.
	_, err := f.repo.GetForItem(testCtx(t), itemB, p.ID)
	if !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Errorf("GetForItem(wrong item) = %v, want ErrPhotoNotFound", err)
	}
}

func TestPhotoRepository_AttachToItem_UnknownItemRejected(t *testing.T) {
	f := newPhotoFixture(t)
	uploader := f.seedUser(t)
	p := newPhoto(uploader, "items/x/"+validHash+".jpg")
	if err := f.repo.Create(testCtx(t), p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err := f.repo.AttachToItem(testCtx(t), storagedomain.NewItemID(), p.ID, 0, true)
	if !errors.Is(err, storagedomain.ErrItemNotFound) {
		t.Errorf("AttachToItem(unknown item) = %v, want storagedomain.ErrItemNotFound", err)
	}
}

func TestPhotoRepository_AttachToItem_UnknownPhotoRejected(t *testing.T) {
	f := newPhotoFixture(t)
	uploader := f.seedUser(t)
	itemID := f.seedItem(t, uploader)

	err := f.repo.AttachToItem(testCtx(t), itemID, domain.NewPhotoID(), 0, true)
	if !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Errorf("AttachToItem(unknown photo) = %v, want domain.ErrPhotoNotFound", err)
	}
}

// TestPhotoRepository_Delete_RemovesJunctionThenPhoto proves the ordering
// Sprint 5 reconciliation R6 requires: after Delete, neither the item_photo
// row nor the photo row remains, and a second AttachToItem for the same
// item would see the photo gone.
func TestPhotoRepository_Delete_RemovesJunctionThenPhoto(t *testing.T) {
	f := newPhotoFixture(t)
	uploader := f.seedUser(t)
	itemID := f.seedItem(t, uploader)
	p := newPhoto(uploader, "items/d/"+validHash+".jpg")
	if err := f.repo.Create(testCtx(t), p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.repo.AttachToItem(testCtx(t), itemID, p.ID, 0, true); err != nil {
		t.Fatalf("AttachToItem: %v", err)
	}

	if err := f.repo.Delete(testCtx(t), itemID, p.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := f.repo.GetForItem(testCtx(t), itemID, p.ID); !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Errorf("GetForItem after Delete = %v, want ErrPhotoNotFound", err)
	}
	list, err := f.repo.ListByItem(testCtx(t), itemID)
	if err != nil {
		t.Fatalf("ListByItem after Delete: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("ListByItem after Delete = %v, want empty", list)
	}
}

func TestPhotoRepository_Delete_NotFound(t *testing.T) {
	f := newPhotoFixture(t)
	err := f.repo.Delete(testCtx(t), storagedomain.NewItemID(), domain.NewPhotoID())
	if !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Errorf("Delete(unknown) = %v, want ErrPhotoNotFound", err)
	}
}

// TestPhotoRepository_Delete_SecondJunctionRowBlocksPhotoDelete proves the
// item_photo_photo_id_fkey ON DELETE RESTRICT guard Delete's own doc
// describes: with a second item_photo row still referencing the photo (an
// artificial setup — item-scoped dedup should make this impossible via
// normal AttachToItem calls, but the guard exists for exactly this case),
// deleting the (itemA, photo) link and then the photo row itself must fail
// rather than silently leaving itemB's junction row dangling.
func TestPhotoRepository_Delete_SecondJunctionRowBlocksPhotoDelete(t *testing.T) {
	f := newPhotoFixture(t)
	uploader := f.seedUser(t)
	itemA := f.seedItem(t, uploader)
	itemB := f.seedItem(t, uploader)
	p := newPhoto(uploader, "items/shared/"+validHash+".jpg")
	if err := f.repo.Create(testCtx(t), p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.repo.AttachToItem(testCtx(t), itemA, p.ID, 0, true); err != nil {
		t.Fatalf("AttachToItem(itemA): %v", err)
	}
	if err := f.repo.AttachToItem(testCtx(t), itemB, p.ID, 0, true); err != nil {
		t.Fatalf("AttachToItem(itemB): %v", err)
	}

	err := f.repo.Delete(testCtx(t), itemA, p.ID)
	if !errors.Is(err, domain.ErrInvalidPhoto) {
		t.Fatalf("Delete(photo still linked to itemB) = %v, want ErrInvalidPhoto", err)
	}

	// itemA's own link is gone (the first statement always runs), but the
	// photo row itself must have survived the blocked delete.
	if _, err := f.repo.GetForItem(testCtx(t), itemA, p.ID); !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Errorf("GetForItem(itemA) after blocked delete = %v, want ErrPhotoNotFound (link removed)", err)
	}
	if _, err := f.repo.GetForItem(testCtx(t), itemB, p.ID); err != nil {
		t.Errorf("GetForItem(itemB) after blocked delete = %v, want nil (photo row survives)", err)
	}
}

// TestPhotoRepository_Create_CheckViolationRejected proves an unexpected
// database CHECK violation (never reachable via Photo.Validate, which the
// application layer always runs first) still maps to ErrInvalidPhoto rather
// than an opaque wrapped error.
func TestPhotoRepository_Create_CheckViolationRejected(t *testing.T) {
	f := newPhotoFixture(t)
	uploader := f.seedUser(t)
	p := newPhoto(uploader, "items/bad/"+validHash+".jpg")
	p.SizeBytes = 0 // violates photo_size_bytes_check

	err := f.repo.Create(testCtx(t), p)
	if !errors.Is(err, domain.ErrInvalidPhoto) {
		t.Errorf("Create(size_bytes=0) = %v, want ErrInvalidPhoto", err)
	}
}

// TestPhotoRepository_SetPrimary_MovesPrimaryFlag proves the
// item_photo_primary_uniq partial unique index is honored: SetPrimary
// clears the previous primary before setting the new one.
func TestPhotoRepository_SetPrimary_MovesPrimaryFlag(t *testing.T) {
	f := newPhotoFixture(t)
	uploader := f.seedUser(t)
	itemID := f.seedItem(t, uploader)
	p1 := newPhoto(uploader, "items/sp/1-"+validHash+".jpg")
	p2 := newPhoto(uploader, "items/sp/2-"+validHash+".jpg")
	for _, p := range []*domain.Photo{p1, p2} {
		if err := f.repo.Create(testCtx(t), p); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	if err := f.repo.AttachToItem(testCtx(t), itemID, p1.ID, 0, true); err != nil {
		t.Fatalf("AttachToItem(p1): %v", err)
	}
	if err := f.repo.AttachToItem(testCtx(t), itemID, p2.ID, 1, false); err != nil {
		t.Fatalf("AttachToItem(p2): %v", err)
	}

	if err := f.repo.SetPrimary(testCtx(t), itemID, p2.ID); err != nil {
		t.Fatalf("SetPrimary: %v", err)
	}

	list, err := f.repo.ListByItem(testCtx(t), itemID)
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	var primaryCount int
	for _, ip := range list {
		if ip.IsPrimary {
			primaryCount++
			if ip.Photo.ID != p2.ID {
				t.Errorf("primary photo = %s, want p2 (%s)", ip.Photo.ID, p2.ID)
			}
		}
	}
	if primaryCount != 1 {
		t.Errorf("primary count = %d, want exactly 1", primaryCount)
	}
}

func TestPhotoRepository_SetPrimary_UnattachedPhotoNotFound(t *testing.T) {
	f := newPhotoFixture(t)
	uploader := f.seedUser(t)
	itemID := f.seedItem(t, uploader)

	err := f.repo.SetPrimary(testCtx(t), itemID, domain.NewPhotoID())
	if !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Errorf("SetPrimary(unattached photo) = %v, want ErrPhotoNotFound", err)
	}
}

// TestPhotoRepository_Reorder_RewritesPositions proves the two-phase
// staged-then-final update (see Reorder's own doc) rewrites positions
// without tripping item_photo_position_uniq, including a full reversal
// (every row's position changes).
func TestPhotoRepository_Reorder_RewritesPositions(t *testing.T) {
	f := newPhotoFixture(t)
	uploader := f.seedUser(t)
	itemID := f.seedItem(t, uploader)
	p1 := newPhoto(uploader, "items/ro/1-"+validHash+".jpg")
	p2 := newPhoto(uploader, "items/ro/2-"+validHash+".jpg")
	p3 := newPhoto(uploader, "items/ro/3-"+validHash+".jpg")
	for i, p := range []*domain.Photo{p1, p2, p3} {
		if err := f.repo.Create(testCtx(t), p); err != nil {
			t.Fatalf("Create(%d): %v", i, err)
		}
		if err := f.repo.AttachToItem(testCtx(t), itemID, p.ID, i, i == 0); err != nil {
			t.Fatalf("AttachToItem(%d): %v", i, err)
		}
	}

	// Full reversal: every row's position changes.
	if err := f.repo.Reorder(testCtx(t), itemID, []domain.PhotoID{p3.ID, p2.ID, p1.ID}); err != nil {
		t.Fatalf("Reorder: %v", err)
	}

	list, err := f.repo.ListByItem(testCtx(t), itemID)
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("ListByItem returned %d photos, want 3", len(list))
	}
	want := []domain.PhotoID{p3.ID, p2.ID, p1.ID}
	for i, ip := range list {
		if ip.Position != i {
			t.Errorf("list[%d].Position = %d, want %d", i, ip.Position, i)
		}
		if ip.Photo.ID != want[i] {
			t.Errorf("list[%d].Photo.ID = %s, want %s", i, ip.Photo.ID, want[i])
		}
	}
}

func TestNewPhotoRepository_NilExecutorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewPhotoRepository(nil) did not panic")
		}
	}()
	adapter.NewPhotoRepository(nil)
}
