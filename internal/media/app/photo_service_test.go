package app_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/media/app"
	"github.com/ericfisherdev/nestorage/internal/media/domain"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// --- fakes -------------------------------------------------------------

// fakeItemGetter is the itemGetter test double: a map of itemID -> either a
// visible item or a nil (not-found/not-visible).
type fakeItemGetter struct {
	visible map[storagedomain.ItemID]bool
}

func newFakeItemGetter(visibleIDs ...storagedomain.ItemID) *fakeItemGetter {
	f := &fakeItemGetter{visible: make(map[storagedomain.ItemID]bool)}
	for _, id := range visibleIDs {
		f.visible[id] = true
	}
	return f
}

func (f *fakeItemGetter) Get(_ context.Context, _ identity.Principal, itemID storagedomain.ItemID) (*storagedomain.Item, error) {
	if !f.visible[itemID] {
		return nil, storagedomain.ErrItemNotFound
	}
	return &storagedomain.Item{ID: itemID}, nil
}

// fakeValidator is the domain.PhotoValidator test double: it stages r into
// a real temp file (so PhotoService's os.ReadFile call behaves exactly as it
// would against the real adapter), or returns a canned error.
type fakeValidator struct {
	err         error
	contentType string
}

func (v *fakeValidator) ValidateAndStage(_ context.Context, r io.Reader, _ int64) (domain.StagedUpload, error) {
	if v.err != nil {
		return domain.StagedUpload{}, v.err
	}
	tmp, err := os.CreateTemp("", "fake-staged-*.tmp")
	if err != nil {
		return domain.StagedUpload{}, err
	}
	defer func() { _ = tmp.Close() }()
	n, err := io.Copy(tmp, r)
	if err != nil {
		return domain.StagedUpload{}, err
	}
	contentType := v.contentType
	if contentType == "" {
		contentType = domain.ContentTypeJPEG
	}
	return domain.StagedUpload{Path: tmp.Name(), ContentType: contentType, SizeBytes: n}, nil
}

// fakeScrubber is the domain.PhotoScrubber test double: a pass-through by
// default. Upload's test fixtures below are plain byte strings ("some image
// bytes", "x", ...), not real JPEG/PNG data, so the real ExifScrubber cannot
// run against them — this fake stands in for the whole dependency the same
// way fakeValidator stands in for PhotoValidator. recordedData/
// recordedContentType capture the last call's arguments for assertions; err
// forces a canned failure.
type fakeScrubber struct {
	err                 error
	recordedData        []byte
	recordedContentType string
}

func (s *fakeScrubber) Scrub(_ context.Context, data []byte, contentType string) ([]byte, error) {
	s.recordedData = append([]byte(nil), data...)
	s.recordedContentType = contentType
	if s.err != nil {
		return nil, s.err
	}
	return data, nil
}

// fakeThumbnailer is the domain.PhotoThumbnailer test double: succeeds by
// default, producing a distinguishable ("thumb:"-prefixed) byte string
// rather than data unchanged, so a test can tell the thumbnail object apart
// from the full one in fakeStore.objects. err forces a canned failure —
// Upload's own doc requires that NOT fail the upload (see
// TestPhotoService_Upload_ThumbnailGenerationFailureDoesNotFailUpload).
type fakeThumbnailer struct {
	err   error
	calls int
}

func (t *fakeThumbnailer) Thumbnail(data []byte, contentType string) (domain.ThumbResult, error) {
	t.calls++
	if t.err != nil {
		return domain.ThumbResult{}, t.err
	}
	thumbBytes := append([]byte("thumb:"), data...)
	return domain.ThumbResult{Bytes: thumbBytes, ContentType: contentType, Width: 1, Height: 1}, nil
}

// fakeStore is the domain.PhotoStore test double: an in-memory ref -> bytes
// map, recording the last Put call's arguments for assertions.
type fakeStore struct {
	objects        map[domain.StorageRef][]byte
	putErr         error
	thumbPutErr    error // fails only a PhotoVariantThumb Put, leaving the full-image Put unaffected
	deleteErr      error
	urlErr         error
	supportsDirect bool
	lastItemID     storagedomain.ItemID
	lastMeta       domain.PutMeta
	deletedRefs    []domain.StorageRef
}

func newFakeStore() *fakeStore { return &fakeStore{objects: make(map[domain.StorageRef][]byte)} }

// Put mirrors the real adapters' variant-aware key layout (buildStorageKey,
// NSTR-84): a PhotoVariantThumb Put lands on its own "_thumb"-suffixed key
// derived from the same meta.ContentHash, rather than colliding with (and
// silently overwriting) the full image's key.
func (s *fakeStore) Put(_ context.Context, itemID storagedomain.ItemID, meta domain.PutMeta, r io.Reader) (domain.StorageRef, error) {
	if s.putErr != nil {
		return "", s.putErr
	}
	if meta.Variant == domain.PhotoVariantThumb && s.thumbPutErr != nil {
		return "", s.thumbPutErr
	}
	s.lastItemID, s.lastMeta = itemID, meta
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	key := meta.ContentHash
	if meta.Variant == domain.PhotoVariantThumb {
		key += "_thumb"
	}
	ref := domain.StorageRef("items/" + itemID.String() + "/" + key)
	s.objects[ref] = data
	return ref, nil
}

func (s *fakeStore) Open(_ context.Context, ref domain.StorageRef) (io.ReadCloser, error) {
	data, ok := s.objects[ref]
	if !ok {
		return nil, domain.ErrPhotoNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *fakeStore) Delete(_ context.Context, ref domain.StorageRef) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deletedRefs = append(s.deletedRefs, ref)
	delete(s.objects, ref)
	return nil
}

func (s *fakeStore) URL(_ context.Context, ref domain.StorageRef, _ time.Duration) (string, error) {
	if s.urlErr != nil {
		return "", s.urlErr
	}
	if _, ok := s.objects[ref]; !ok {
		return "", domain.ErrPhotoNotFound
	}
	return "https://example.test/" + string(ref), nil
}

func (s *fakeStore) SupportsDirectURL() bool { return s.supportsDirect }

// fakeRepo is the domain.PhotoRepository test double: in-memory photos plus
// item_photo attachments, recording call order for TestPhotoService_Delete_Ordering.
type fakeRepo struct {
	photos         map[domain.PhotoID]*domain.Photo
	byRef          map[domain.StorageRef]domain.PhotoID
	attachments    map[storagedomain.ItemID][]domain.ItemPhoto
	createErr      error
	findErr        error
	listErr        error
	attachErr      error
	listPrimaryErr error
	events         *[]string
}

func newFakeRepo(events *[]string) *fakeRepo {
	return &fakeRepo{
		photos:      make(map[domain.PhotoID]*domain.Photo),
		byRef:       make(map[domain.StorageRef]domain.PhotoID),
		attachments: make(map[storagedomain.ItemID][]domain.ItemPhoto),
		events:      events,
	}
}

func (r *fakeRepo) log(event string) {
	if r.events != nil {
		*r.events = append(*r.events, event)
	}
}

func (r *fakeRepo) Create(_ context.Context, photo *domain.Photo) error {
	r.log("repo.Create")
	if r.createErr != nil {
		return r.createErr
	}
	photo.CreatedAt = time.Now()
	cp := *photo
	r.photos[photo.ID] = &cp
	r.byRef[photo.StorageRef] = photo.ID
	return nil
}

func (r *fakeRepo) GetForItem(_ context.Context, itemID storagedomain.ItemID, id domain.PhotoID) (*domain.Photo, error) {
	for _, ip := range r.attachments[itemID] {
		if ip.Photo.ID == id {
			p := ip.Photo
			return &p, nil
		}
	}
	return nil, domain.ErrPhotoNotFound
}

func (r *fakeRepo) FindByStorageRef(_ context.Context, ref domain.StorageRef) (*domain.Photo, error) {
	if r.findErr != nil {
		return nil, r.findErr
	}
	id, ok := r.byRef[ref]
	if !ok {
		return nil, domain.ErrPhotoNotFound
	}
	return r.photos[id], nil
}

func (r *fakeRepo) AttachToItem(_ context.Context, itemID storagedomain.ItemID, photoID domain.PhotoID, position int, isPrimary bool) error {
	r.log("repo.AttachToItem")
	if r.attachErr != nil {
		return r.attachErr
	}
	p, ok := r.photos[photoID]
	if !ok {
		return domain.ErrPhotoNotFound
	}
	r.attachments[itemID] = append(r.attachments[itemID], domain.ItemPhoto{Photo: *p, Position: position, IsPrimary: isPrimary})
	return nil
}

func (r *fakeRepo) ListByItem(_ context.Context, itemID storagedomain.ItemID) ([]domain.ItemPhoto, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]domain.ItemPhoto(nil), r.attachments[itemID]...), nil
}

func (r *fakeRepo) Delete(_ context.Context, itemID storagedomain.ItemID, id domain.PhotoID) error {
	r.log("repo.Delete")
	if _, ok := r.photos[id]; !ok {
		return domain.ErrPhotoNotFound
	}
	delete(r.photos, id)
	list := r.attachments[itemID]
	filtered := make([]domain.ItemPhoto, 0, len(list))
	for _, ip := range list {
		if ip.Photo.ID != id {
			filtered = append(filtered, ip)
		}
	}
	r.attachments[itemID] = filtered
	return nil
}

func (r *fakeRepo) SetPrimary(_ context.Context, itemID storagedomain.ItemID, photoID domain.PhotoID) error {
	r.log("repo.SetPrimary")
	found := false
	list := r.attachments[itemID]
	for i := range list {
		if list[i].Photo.ID == photoID {
			found = true
		}
	}
	if !found {
		return domain.ErrPhotoNotFound
	}
	for i := range list {
		list[i].IsPrimary = list[i].Photo.ID == photoID
	}
	return nil
}

func (r *fakeRepo) Reorder(_ context.Context, itemID storagedomain.ItemID, order []domain.PhotoID) error {
	r.log("repo.Reorder")
	byID := make(map[domain.PhotoID]domain.ItemPhoto, len(r.attachments[itemID]))
	for _, ip := range r.attachments[itemID] {
		byID[ip.Photo.ID] = ip
	}
	reordered := make([]domain.ItemPhoto, 0, len(order))
	for i, id := range order {
		ip, ok := byID[id]
		if !ok {
			return domain.ErrPhotoNotFound
		}
		ip.Position = i
		reordered = append(reordered, ip)
	}
	r.attachments[itemID] = reordered
	return nil
}

// SetThumbnailRef and ListMissingThumbnail satisfy domain.PhotoRepository's
// NSTR-84 additions — PhotoService itself never calls either (they exist for
// cmd/backfill-thumbs), so these are the minimal stubs interface compliance
// needs, not exercised by any PhotoService test in this file.
func (r *fakeRepo) SetThumbnailRef(_ context.Context, photoID domain.PhotoID, ref domain.StorageRef) error {
	p, ok := r.photos[photoID]
	if !ok {
		return domain.ErrPhotoNotFound
	}
	p.ThumbnailRef = &ref
	return nil
}

func (r *fakeRepo) ListMissingThumbnail(_ context.Context, _ int, _ domain.PhotoID) ([]domain.MissingThumbnailPhoto, error) {
	return nil, nil
}

// ListPrimaryForItems backs TestPhotoService_ListPrimaryThumbRefs*: scans
// each requested item's attachments for its primary photo, mirroring the
// real adapter's "absent from the map when there is none" contract.
func (r *fakeRepo) ListPrimaryForItems(_ context.Context, itemIDs []storagedomain.ItemID) (map[storagedomain.ItemID]domain.PhotoID, error) {
	if r.listPrimaryErr != nil {
		return nil, r.listPrimaryErr
	}
	out := make(map[storagedomain.ItemID]domain.PhotoID, len(itemIDs))
	for _, itemID := range itemIDs {
		for _, ip := range r.attachments[itemID] {
			if ip.IsPrimary {
				out[itemID] = ip.Photo.ID
				break
			}
		}
	}
	return out, nil
}

// --- helpers -------------------------------------------------------------

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func newViewer() identity.Principal {
	return identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Tester")
}

// --- tests -----------------------------------------------------------------

func TestPhotoService_Upload_ItemNotVisible(t *testing.T) {
	items := newFakeItemGetter() // nothing visible
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, newFakeRepo(nil), items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}

	_, err = svc.Upload(context.Background(), newViewer(), storagedomain.NewItemID(), bytes.NewReader([]byte("data")))
	if !errors.Is(err, storagedomain.ErrItemNotFound) {
		t.Fatalf("Upload(invisible item) = %v, want storagedomain.ErrItemNotFound", err)
	}
}

func TestPhotoService_Upload_ValidatorErrorPropagates(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	wantErr := domain.ErrUnsupportedMediaType
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{err: wantErr}, &fakeScrubber{}, &fakeThumbnailer{}, newFakeRepo(nil), items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}

	_, err = svc.Upload(context.Background(), newViewer(), itemID, bytes.NewReader([]byte("data")))
	if !errors.Is(err, wantErr) {
		t.Fatalf("Upload(validator error) = %v, want %v", err, wantErr)
	}
}

func TestPhotoService_Upload_Success(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	store := newFakeStore()
	repo := newFakeRepo(nil)
	svc, err := app.NewPhotoService(store, &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, repo, items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	viewer := newViewer()

	photo, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader([]byte("some image bytes")))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if photo.UploadedBy != viewer.UserID {
		t.Errorf("Upload photo.UploadedBy = %v, want %v", photo.UploadedBy, viewer.UserID)
	}
	if store.lastItemID != itemID {
		t.Errorf("Put itemID = %v, want %v", store.lastItemID, itemID)
	}
	if store.lastMeta.ContentType != domain.ContentTypeJPEG {
		t.Errorf("Put meta.ContentType = %q, want %q", store.lastMeta.ContentType, domain.ContentTypeJPEG)
	}

	list, err := svc.ListForItem(context.Background(), viewer, itemID)
	if err != nil {
		t.Fatalf("ListForItem: %v", err)
	}
	if len(list) != 1 || list[0].Photo.ID != photo.ID || !list[0].IsPrimary || list[0].Position != 0 {
		t.Errorf("ListForItem = %+v, want the uploaded photo attached primary at position 0", list)
	}
}

func TestPhotoService_Upload_DedupReturnsExistingWithoutCreate(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	store := newFakeStore()
	repo := newFakeRepo(nil)
	svc, err := app.NewPhotoService(store, &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, repo, items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	viewer := newViewer()
	data := []byte("identical bytes")

	first, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("first Upload: %v", err)
	}
	second, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("second Upload: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("second Upload returned a new photo %v, want the existing %v", second.ID, first.ID)
	}

	list, err := svc.ListForItem(context.Background(), viewer, itemID)
	if err != nil {
		t.Fatalf("ListForItem: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("ListForItem returned %d photos after a duplicate upload, want 1", len(list))
	}
}

func TestPhotoService_Open(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	store := newFakeStore()
	repo := newFakeRepo(nil)
	svc, err := app.NewPhotoService(store, &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, repo, items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	viewer := newViewer()
	data := []byte("readable bytes")
	photo, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	rc, contentType, err := svc.Open(context.Background(), viewer, itemID, photo.ID, domain.PhotoVariantFull)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("Open returned %q, want %q", got, data)
	}
	if contentType != domain.ContentTypeJPEG {
		t.Errorf("Open contentType = %q, want %q", contentType, domain.ContentTypeJPEG)
	}
}

func TestPhotoService_Open_WrongItemNotFound(t *testing.T) {
	itemA := storagedomain.NewItemID()
	itemB := storagedomain.NewItemID()
	items := newFakeItemGetter(itemA, itemB)
	store := newFakeStore()
	repo := newFakeRepo(nil)
	svc, err := app.NewPhotoService(store, &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, repo, items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	viewer := newViewer()
	photo, err := svc.Upload(context.Background(), viewer, itemA, bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	// itemB is visible to the viewer, but photo belongs to itemA — must
	// still be reported not found (Sprint 5 reconciliation R5).
	if _, _, err := svc.Open(context.Background(), viewer, itemB, photo.ID, domain.PhotoVariantFull); !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Fatalf("Open(wrong item) = %v, want ErrPhotoNotFound", err)
	}
}

func TestPhotoService_DirectURLAndSupportsDirectURL(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	store := newFakeStore()
	store.supportsDirect = true
	repo := newFakeRepo(nil)
	svc, err := app.NewPhotoService(store, &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, repo, items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	if !svc.SupportsDirectURL() {
		t.Fatal("SupportsDirectURL() = false, want true")
	}
	viewer := newViewer()
	photo, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	url, err := svc.DirectURL(context.Background(), viewer, itemID, photo.ID, domain.PhotoVariantFull)
	if err != nil {
		t.Fatalf("DirectURL: %v", err)
	}
	if url == "" {
		t.Error("DirectURL returned an empty URL")
	}
}

// TestPhotoService_Delete_Ordering proves the repository row(s) are removed
// before the stored object(s) (Sprint 5 reconciliation R6), and that both
// the thumbnail and full objects are removed, thumbnail first (NSTR-84).
func TestPhotoService_Delete_Ordering(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	store := newFakeStore()
	var events []string
	repo := newFakeRepo(&events)
	svc, err := app.NewPhotoService(store, &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, repo, items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	viewer := newViewer()
	photo, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if photo.ThumbnailRef == nil {
		t.Fatal("Upload left ThumbnailRef nil, want the fakeThumbnailer's default success to have populated it")
	}
	events = nil // reset: only care about Delete's own ordering

	if err := svc.Delete(context.Background(), viewer, itemID, photo.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(events) != 1 || events[0] != "repo.Delete" {
		t.Errorf("events = %v, want exactly [repo.Delete]", events)
	}
	wantDeleted := []domain.StorageRef{*photo.ThumbnailRef, photo.StorageRef}
	if len(store.deletedRefs) != len(wantDeleted) || store.deletedRefs[0] != wantDeleted[0] || store.deletedRefs[1] != wantDeleted[1] {
		t.Errorf("store.deletedRefs = %v, want %v (thumbnail before full)", store.deletedRefs, wantDeleted)
	}

	if _, _, err := svc.Open(context.Background(), viewer, itemID, photo.ID, domain.PhotoVariantFull); !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Errorf("Open after Delete = %v, want ErrPhotoNotFound", err)
	}
}

func TestPhotoService_Delete_NotVisibleItem(t *testing.T) {
	items := newFakeItemGetter()
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, newFakeRepo(nil), items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	err = svc.Delete(context.Background(), newViewer(), storagedomain.NewItemID(), domain.NewPhotoID())
	if !errors.Is(err, storagedomain.ErrItemNotFound) {
		t.Fatalf("Delete(invisible item) = %v, want storagedomain.ErrItemNotFound", err)
	}
}

func TestPhotoService_SetPrimary(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	store := newFakeStore()
	repo := newFakeRepo(nil)
	svc, err := app.NewPhotoService(store, &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, repo, items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	viewer := newViewer()
	p1, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader([]byte("one")))
	if err != nil {
		t.Fatalf("Upload(p1): %v", err)
	}
	p2, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader([]byte("two")))
	if err != nil {
		t.Fatalf("Upload(p2): %v", err)
	}

	if err := svc.SetPrimary(context.Background(), viewer, itemID, p2.ID); err != nil {
		t.Fatalf("SetPrimary: %v", err)
	}

	list, err := svc.ListForItem(context.Background(), viewer, itemID)
	if err != nil {
		t.Fatalf("ListForItem: %v", err)
	}
	for _, ip := range list {
		if ip.Photo.ID == p1.ID && ip.IsPrimary {
			t.Error("p1 must no longer be primary after SetPrimary(p2)")
		}
		if ip.Photo.ID == p2.ID && !ip.IsPrimary {
			t.Error("p2 must be primary after SetPrimary(p2)")
		}
	}
}

func TestPhotoService_Reorder(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	store := newFakeStore()
	repo := newFakeRepo(nil)
	svc, err := app.NewPhotoService(store, &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, repo, items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	viewer := newViewer()
	p1, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader([]byte("one")))
	if err != nil {
		t.Fatalf("Upload(p1): %v", err)
	}
	p2, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader([]byte("two")))
	if err != nil {
		t.Fatalf("Upload(p2): %v", err)
	}

	if err := svc.Reorder(context.Background(), viewer, itemID, []domain.PhotoID{p2.ID, p1.ID}); err != nil {
		t.Fatalf("Reorder: %v", err)
	}

	list, err := svc.ListForItem(context.Background(), viewer, itemID)
	if err != nil {
		t.Fatalf("ListForItem: %v", err)
	}
	if len(list) != 2 || list[0].Photo.ID != p2.ID || list[1].Photo.ID != p1.ID {
		t.Errorf("ListForItem after Reorder = %+v, want [p2, p1]", list)
	}
}

func TestPhotoService_Reorder_NotVisibleItem(t *testing.T) {
	items := newFakeItemGetter()
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, newFakeRepo(nil), items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	err = svc.Reorder(context.Background(), newViewer(), storagedomain.NewItemID(), nil)
	if !errors.Is(err, storagedomain.ErrItemNotFound) {
		t.Fatalf("Reorder(invisible item) = %v, want storagedomain.ErrItemNotFound", err)
	}
}

func TestPhotoService_ListForItem_NotVisibleItem(t *testing.T) {
	items := newFakeItemGetter()
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, newFakeRepo(nil), items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	_, err = svc.ListForItem(context.Background(), newViewer(), storagedomain.NewItemID())
	if !errors.Is(err, storagedomain.ErrItemNotFound) {
		t.Fatalf("ListForItem(invisible item) = %v, want storagedomain.ErrItemNotFound", err)
	}
}

func TestPhotoService_ListForItem_RepoErrorPropagates(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	repo := newFakeRepo(nil)
	repo.listErr = errors.New("boom")
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, repo, items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	if _, err := svc.ListForItem(context.Background(), newViewer(), itemID); err == nil {
		t.Fatal("ListForItem(repo error) = nil, want an error")
	}
}

func TestPhotoService_SetPrimary_NotVisibleItem(t *testing.T) {
	items := newFakeItemGetter()
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, newFakeRepo(nil), items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	err = svc.SetPrimary(context.Background(), newViewer(), storagedomain.NewItemID(), domain.NewPhotoID())
	if !errors.Is(err, storagedomain.ErrItemNotFound) {
		t.Fatalf("SetPrimary(invisible item) = %v, want storagedomain.ErrItemNotFound", err)
	}
}

func TestPhotoService_DirectURL_NotVisibleItem(t *testing.T) {
	items := newFakeItemGetter()
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, newFakeRepo(nil), items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	_, err = svc.DirectURL(context.Background(), newViewer(), storagedomain.NewItemID(), domain.NewPhotoID(), domain.PhotoVariantFull)
	if !errors.Is(err, storagedomain.ErrItemNotFound) {
		t.Fatalf("DirectURL(invisible item) = %v, want storagedomain.ErrItemNotFound", err)
	}
}

func TestPhotoService_DirectURL_StoreErrorPropagates(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	store := newFakeStore()
	repo := newFakeRepo(nil)
	svc, err := app.NewPhotoService(store, &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, repo, items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	viewer := newViewer()
	photo, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	store.urlErr = errors.New("boom")

	if _, err := svc.DirectURL(context.Background(), viewer, itemID, photo.ID, domain.PhotoVariantFull); err == nil {
		t.Fatal("DirectURL(store error) = nil, want an error")
	}
}

func TestPhotoService_Delete_StoreErrorPropagates(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	store := newFakeStore()
	repo := newFakeRepo(nil)
	svc, err := app.NewPhotoService(store, &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, repo, items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	viewer := newViewer()
	photo, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	store.deleteErr = errors.New("boom")

	if err := svc.Delete(context.Background(), viewer, itemID, photo.ID); err == nil {
		t.Fatal("Delete(store delete error) = nil, want an error")
	}
}

// TestPhotoService_Upload_FindByStorageRefUnexpectedErrorPropagates covers
// Upload's dedup probe: an error other than ErrPhotoNotFound from
// FindByStorageRef must propagate rather than being treated as "not a
// duplicate."
func TestPhotoService_Upload_FindByStorageRefUnexpectedErrorPropagates(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	repo := newFakeRepo(nil)
	repo.findErr = errors.New("boom")
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, repo, items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	if _, err := svc.Upload(context.Background(), newViewer(), itemID, bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("Upload(FindByStorageRef error) = nil, want an error")
	}
}

// TestPhotoService_Upload_CreateErrorPropagates covers a Create failure
// other than ErrDuplicatePhoto (the one Create error Upload resolves
// itself — see TestPhotoService_Upload_DedupReturnsExistingWithoutCreate).
func TestPhotoService_Upload_CreateErrorPropagates(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	repo := newFakeRepo(nil)
	repo.createErr = errors.New("boom")
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, repo, items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	if _, err := svc.Upload(context.Background(), newViewer(), itemID, bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("Upload(Create error) = nil, want an error")
	}
}

func TestPhotoService_Upload_ListByItemErrorPropagates(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	repo := newFakeRepo(nil)
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, repo, items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	repo.listErr = errors.New("boom")
	if _, err := svc.Upload(context.Background(), newViewer(), itemID, bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("Upload(ListByItem error) = nil, want an error")
	}
}

func TestPhotoService_Upload_AttachToItemErrorPropagates(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	repo := newFakeRepo(nil)
	repo.attachErr = errors.New("boom")
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, repo, items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	if _, err := svc.Upload(context.Background(), newViewer(), itemID, bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("Upload(AttachToItem error) = nil, want an error")
	}
}

func TestNewPhotoService_NilDependenciesPanic(t *testing.T) {
	store := newFakeStore()
	validator := &fakeValidator{}
	scrubber := &fakeScrubber{}
	thumbnailer := &fakeThumbnailer{}
	repo := newFakeRepo(nil)
	items := newFakeItemGetter()
	logger := testLogger()

	cases := []struct {
		name string
		fn   func()
	}{
		{"nil store", func() { _, _ = app.NewPhotoService(nil, validator, scrubber, thumbnailer, repo, items, 1, logger) }},
		{"nil validator", func() { _, _ = app.NewPhotoService(store, nil, scrubber, thumbnailer, repo, items, 1, logger) }},
		{"nil scrubber", func() { _, _ = app.NewPhotoService(store, validator, nil, thumbnailer, repo, items, 1, logger) }},
		{"nil thumbnailer", func() { _, _ = app.NewPhotoService(store, validator, scrubber, nil, repo, items, 1, logger) }},
		{"nil repo", func() { _, _ = app.NewPhotoService(store, validator, scrubber, thumbnailer, nil, items, 1, logger) }},
		{"nil items", func() { _, _ = app.NewPhotoService(store, validator, scrubber, thumbnailer, repo, nil, 1, logger) }},
		{"nil logger", func() { _, _ = app.NewPhotoService(store, validator, scrubber, thumbnailer, repo, items, 1, nil) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: did not panic", c.name)
				}
			}()
			c.fn()
		})
	}
}

func TestNewPhotoService_NonPositiveMaxUploadBytesRejected(t *testing.T) {
	_, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, newFakeRepo(nil), newFakeItemGetter(), 0, testLogger())
	if err == nil {
		t.Error("NewPhotoService(maxUploadBytes=0) error = nil, want an error")
	}
}

// --- NSTR-84: thumbnail generation, variant serving, and dual-object delete ---

// TestPhotoService_Upload_GeneratesAndStoresThumbnail proves a successful
// upload generates a thumbnail from the scrubbed bytes and stores it under
// its own key (via fakeStore's variant-aware Put), recording the resulting
// ref on the created Photo.
func TestPhotoService_Upload_GeneratesAndStoresThumbnail(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	store := newFakeStore()
	thumbnailer := &fakeThumbnailer{}
	repo := newFakeRepo(nil)
	svc, err := app.NewPhotoService(store, &fakeValidator{}, &fakeScrubber{}, thumbnailer, repo, items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}

	photo, err := svc.Upload(context.Background(), newViewer(), itemID, bytes.NewReader([]byte("some image bytes")))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if thumbnailer.calls != 1 {
		t.Errorf("thumbnailer.calls = %d, want exactly 1", thumbnailer.calls)
	}
	if photo.ThumbnailRef == nil {
		t.Fatal("Upload left ThumbnailRef nil, want it populated")
	}
	thumbBytes, ok := store.objects[*photo.ThumbnailRef]
	if !ok {
		t.Fatal("no object stored under the returned ThumbnailRef")
	}
	if string(thumbBytes) != "thumb:some image bytes" {
		t.Errorf("stored thumbnail bytes = %q, want the fakeThumbnailer's transformed output", thumbBytes)
	}
	if *photo.ThumbnailRef == photo.StorageRef {
		t.Error("ThumbnailRef must differ from the full image's StorageRef")
	}
}

// TestPhotoService_Upload_ThumbnailGenerationFailureDoesNotFailUpload covers
// Upload's own doc: a photo whose thumbnail could not be generated is still
// stored (with a nil ThumbnailRef), never a failed upload.
func TestPhotoService_Upload_ThumbnailGenerationFailureDoesNotFailUpload(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	thumbnailer := &fakeThumbnailer{err: errors.New("boom")}
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, thumbnailer, newFakeRepo(nil), items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}

	photo, err := svc.Upload(context.Background(), newViewer(), itemID, bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("Upload(thumbnail generation error) = %v, want nil (non-fatal)", err)
	}
	if photo.ThumbnailRef != nil {
		t.Errorf("ThumbnailRef = %v, want nil after a thumbnail generation failure", *photo.ThumbnailRef)
	}
}

// TestPhotoService_Upload_ThumbnailStorageFailureDoesNotFailUpload covers
// the other non-fatal path: generation succeeds but storing the result
// fails.
func TestPhotoService_Upload_ThumbnailStorageFailureDoesNotFailUpload(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	store := newFakeStore()
	store.thumbPutErr = errors.New("boom")
	svc, err := app.NewPhotoService(store, &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, newFakeRepo(nil), items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}

	photo, err := svc.Upload(context.Background(), newViewer(), itemID, bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("Upload(thumbnail storage error) = %v, want nil (non-fatal)", err)
	}
	if photo.ThumbnailRef != nil {
		t.Errorf("ThumbnailRef = %v, want nil after a thumbnail storage failure", *photo.ThumbnailRef)
	}
	// The full image must still be stored and readable.
	if _, ok := store.objects[photo.StorageRef]; !ok {
		t.Error("full image object missing after a thumbnail storage failure")
	}
}

// TestPhotoService_Upload_DedupSkipsThumbnailGeneration proves the
// thumbnailer is never invoked for a duplicate re-upload that resolves to
// an existing photo — generating a thumbnail nobody will use would be
// wasted CPU.
func TestPhotoService_Upload_DedupSkipsThumbnailGeneration(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	thumbnailer := &fakeThumbnailer{}
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, thumbnailer, newFakeRepo(nil), items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	viewer := newViewer()
	data := []byte("identical bytes")

	if _, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader(data)); err != nil {
		t.Fatalf("first Upload: %v", err)
	}
	if thumbnailer.calls != 1 {
		t.Fatalf("thumbnailer.calls after first Upload = %d, want 1", thumbnailer.calls)
	}
	if _, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader(data)); err != nil {
		t.Fatalf("second Upload: %v", err)
	}
	if thumbnailer.calls != 1 {
		t.Errorf("thumbnailer.calls after a duplicate re-upload = %d, want still 1", thumbnailer.calls)
	}
}

// TestPhotoService_Open_ThumbVariantReturnsThumbnailBytes proves requesting
// PhotoVariantThumb streams the thumbnail object, not the full one.
func TestPhotoService_Open_ThumbVariantReturnsThumbnailBytes(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, newFakeRepo(nil), items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	viewer := newViewer()
	photo, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader([]byte("original")))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	rc, _, err := svc.Open(context.Background(), viewer, itemID, photo.ID, domain.PhotoVariantThumb)
	if err != nil {
		t.Fatalf("Open(thumb): %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "thumb:original" {
		t.Errorf("Open(thumb) returned %q, want the thumbnail bytes", got)
	}
}

// TestPhotoService_Open_ThumbVariantFallsBackWhenNil proves a thumb request
// against a photo with a nil ThumbnailRef transparently serves the full
// image instead — no caller has to implement this fallback.
func TestPhotoService_Open_ThumbVariantFallsBackWhenNil(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	thumbnailer := &fakeThumbnailer{err: errors.New("boom")} // forces a nil ThumbnailRef
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, thumbnailer, newFakeRepo(nil), items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	viewer := newViewer()
	photo, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader([]byte("original")))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if photo.ThumbnailRef != nil {
		t.Fatalf("Upload.ThumbnailRef = %v, want nil (setup precondition)", *photo.ThumbnailRef)
	}

	rc, _, err := svc.Open(context.Background(), viewer, itemID, photo.ID, domain.PhotoVariantThumb)
	if err != nil {
		t.Fatalf("Open(thumb, fallback): %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "original" {
		t.Errorf("Open(thumb, fallback) returned %q, want the full image's bytes", got)
	}
}

// TestPhotoService_DirectURL_ThumbVariant proves DirectURL resolves the
// same variant/fallback rule Open does.
func TestPhotoService_DirectURL_ThumbVariant(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	store := newFakeStore()
	store.supportsDirect = true
	svc, err := app.NewPhotoService(store, &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, newFakeRepo(nil), items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	viewer := newViewer()
	photo, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader([]byte("original")))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	url, err := svc.DirectURL(context.Background(), viewer, itemID, photo.ID, domain.PhotoVariantThumb)
	if err != nil {
		t.Fatalf("DirectURL(thumb): %v", err)
	}
	want := "https://example.test/" + photo.ThumbnailRef.String()
	if url != want {
		t.Errorf("DirectURL(thumb) = %q, want %q", url, want)
	}
}

// TestPhotoService_Delete_RemovesFullOnlyWhenThumbnailRefNil proves Delete
// does not attempt to remove a thumbnail object that was never stored.
func TestPhotoService_Delete_RemovesFullOnlyWhenThumbnailRefNil(t *testing.T) {
	itemID := storagedomain.NewItemID()
	items := newFakeItemGetter(itemID)
	store := newFakeStore()
	thumbnailer := &fakeThumbnailer{err: errors.New("boom")} // forces a nil ThumbnailRef
	svc, err := app.NewPhotoService(store, &fakeValidator{}, &fakeScrubber{}, thumbnailer, newFakeRepo(nil), items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	viewer := newViewer()
	photo, err := svc.Upload(context.Background(), viewer, itemID, bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	if err := svc.Delete(context.Background(), viewer, itemID, photo.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(store.deletedRefs) != 1 || store.deletedRefs[0] != photo.StorageRef {
		t.Errorf("store.deletedRefs = %v, want exactly [%v]", store.deletedRefs, photo.StorageRef)
	}
}

// --- ListPrimaryThumbRefs (NSTR-37, Sprint 5 reconciliation R8) ----------

func TestPhotoService_ListPrimaryThumbRefs_ReturnsPrimaryOnly(t *testing.T) {
	itemWithPhotos := storagedomain.NewItemID()
	itemWithNone := storagedomain.NewItemID()
	items := newFakeItemGetter(itemWithPhotos, itemWithNone)
	repo := newFakeRepo(nil)
	primary := domain.Photo{ID: domain.NewPhotoID(), StorageRef: "items/a/x.jpg", ContentHash: strings.Repeat("a", 64), SizeBytes: 1, ContentType: domain.ContentTypeJPEG, StorageBackend: domain.StorageBackendLocal}
	secondary := domain.Photo{ID: domain.NewPhotoID(), StorageRef: "items/a/y.jpg", ContentHash: strings.Repeat("b", 64), SizeBytes: 1, ContentType: domain.ContentTypeJPEG, StorageBackend: domain.StorageBackendLocal}
	repo.attachments[itemWithPhotos] = []domain.ItemPhoto{
		{Photo: primary, Position: 0, IsPrimary: true},
		{Photo: secondary, Position: 1, IsPrimary: false},
	}
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, repo, items, 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}

	refs, err := svc.ListPrimaryThumbRefs(context.Background(), newViewer(), []storagedomain.ItemID{itemWithPhotos, itemWithNone})
	if err != nil {
		t.Fatalf("ListPrimaryThumbRefs: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("ListPrimaryThumbRefs returned %d refs, want exactly 1 (the photoless item must be absent)", len(refs))
	}
	got, ok := refs[itemWithPhotos]
	if !ok {
		t.Fatalf("ListPrimaryThumbRefs missing itemWithPhotos entirely")
	}
	if got.PhotoID != primary.ID.String() {
		t.Errorf("ListPrimaryThumbRefs[itemWithPhotos].PhotoID = %q, want the PRIMARY photo's id %q (not the secondary)", got.PhotoID, primary.ID.String())
	}
	if _, ok := refs[itemWithNone]; ok {
		t.Errorf("ListPrimaryThumbRefs must not include an item with no primary photo, got an entry for itemWithNone")
	}
}

func TestPhotoService_ListPrimaryThumbRefs_EmptyItemIDs(t *testing.T) {
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, newFakeRepo(nil), newFakeItemGetter(), 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	refs, err := svc.ListPrimaryThumbRefs(context.Background(), newViewer(), nil)
	if err != nil {
		t.Fatalf("ListPrimaryThumbRefs(nil ids): %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("ListPrimaryThumbRefs(nil ids) = %v, want empty", refs)
	}
}

func TestPhotoService_ListPrimaryThumbRefs_RepoErrorPropagates(t *testing.T) {
	repo := newFakeRepo(nil)
	repo.listPrimaryErr = errors.New("boom")
	svc, err := app.NewPhotoService(newFakeStore(), &fakeValidator{}, &fakeScrubber{}, &fakeThumbnailer{}, repo, newFakeItemGetter(), 10<<20, testLogger())
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	if _, err := svc.ListPrimaryThumbRefs(context.Background(), newViewer(), []storagedomain.ItemID{storagedomain.NewItemID()}); err == nil {
		t.Fatal("ListPrimaryThumbRefs(repo error) = nil, want an error")
	}
}
