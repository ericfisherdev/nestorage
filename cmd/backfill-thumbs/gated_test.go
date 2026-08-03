package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	mediaadapter "github.com/ericfisherdev/nestorage/internal/media/adapter"
	mediadomain "github.com/ericfisherdev/nestorage/internal/media/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
	storageadapter "github.com/ericfisherdev/nestorage/internal/storage/adapter"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// seedHash is a well-formed 64-character lowercase hex string — the shape
// content_sha256 requires — standing in for a real sha256 sum. This test
// never re-derives it from testJPEGBytes; it only needs to be well-formed
// and to appear in the on-disk full image's filename, matching
// buildStorageKey's layout.
const seedHash = "d1b2c3d4e5f60718293a4b5c6d7e8f90112233445566778899aabbccddeeff0"

// TestRun_BackfillsAgainstRealDatabase drives run() end to end against a
// real, isolated database and the local filesystem PhotoStore: a photo row
// seeded with a NULL thumbnail reference gets a real thumbnail generated,
// written to disk under its own "_thumb" key, and recorded on the row by
// the time run() returns — the automated equivalent of this command's own
// "safe to re-run, does not block application start" acceptance criterion.
func TestRun_BackfillsAgainstRealDatabase(t *testing.T) {
	pool := dbtest.Harness.NewIsolatedPool(t, "backfillthumbs")
	dsn := dbtest.Harness.DSN(t, "backfillthumbs") // same suffix: the identical, already-migrated database

	mediaRoot := t.TempDir()
	t.Setenv("DATABASE_URL", dsn)
	// test, not dev: dev would attempt to load a developer's own .env from
	// this package's directory, which this test must not depend on (mirrors
	// cmd/server/gated_test.go's identical rationale).
	t.Setenv("APP_ENV", "test")
	t.Setenv("MEDIA_ROOT", mediaRoot)

	ctx := context.Background()
	locs := storageadapter.NewLocationRepository(pool)
	bins := storageadapter.NewBinRepository(pool)
	items := storageadapter.NewItemRepository(pool)
	photos := mediaadapter.NewPhotoRepository(pool)

	uploader := seedBackfillUploader(ctx, t, pool)
	loc := &storagedomain.Location{ID: storagedomain.NewLocationID(), HouseholdID: uploader.HouseholdID, Name: "Garage", CreatedBy: uploader.ID}
	if err := locs.Create(ctx, loc); err != nil {
		t.Fatalf("seed location: %v", err)
	}
	binID := storagedomain.NewBinID()
	bin := &storagedomain.Bin{
		ID: binID, HouseholdID: uploader.HouseholdID, Code: "BFT" + binID.String(), Name: "Backfill bin",
		LocationID: loc.ID, CreatedBy: uploader.ID, Visibility: storagedomain.VisibilityPublic,
	}
	if err := bins.Create(ctx, bin); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
	item := &storagedomain.Item{
		ID: storagedomain.NewItemID(), HouseholdID: uploader.HouseholdID, Name: "Backfilled item", Quantity: 1,
		CurrentBinID: &bin.ID, CreatedBy: uploader.ID,
	}
	if err := items.Create(ctx, item); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	// Write a real, decodable JPEG at the full image's on-disk path
	// (buildStorageKey's items/<item>/<hash>.<ext> layout), and a matching
	// photo row with a NULL thumbnail reference — exactly the state a
	// pre-NSTR-84 upload left behind.
	ref := mediadomain.StorageRef("items/" + item.ID.String() + "/" + seedHash + ".jpg")
	fullPath := filepath.Join(mediaRoot, "items", item.ID.String(), seedHash+".jpg")
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("mkdir full image dir: %v", err)
	}
	data := testJPEGBytes(t)
	if err := os.WriteFile(fullPath, data, 0o644); err != nil {
		t.Fatalf("write full image: %v", err)
	}

	photo := &mediadomain.Photo{
		ID: mediadomain.NewPhotoID(), HouseholdID: uploader.HouseholdID, StorageRef: ref, ContentHash: seedHash,
		SizeBytes: int64(len(data)), ContentType: mediadomain.ContentTypeJPEG,
		StorageBackend: mediadomain.StorageBackendLocal, UploadedBy: uploader.ID,
	}
	if err := photos.Create(ctx, photo); err != nil {
		t.Fatalf("seed photo: %v", err)
	}
	if err := photos.AttachToItem(ctx, uploader.HouseholdID, item.ID, photo.ID, 0, true); err != nil {
		t.Fatalf("attach photo to item: %v", err)
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if err := run(logger); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}

	got, err := photos.FindByStorageRef(ctx, uploader.HouseholdID, ref)
	if err != nil {
		t.Fatalf("FindByStorageRef after run(): %v", err)
	}
	if got.ThumbnailRef == nil {
		t.Fatal("run() left ThumbnailRef nil, want it populated")
	}
	wantThumbPath := filepath.Join(mediaRoot, "items", item.ID.String(), seedHash+"_thumb.jpg")
	info, err := os.Stat(wantThumbPath)
	if err != nil {
		t.Fatalf("thumbnail file not found on disk at %s: %v", wantThumbPath, err)
	}
	if info.Size() == 0 || info.Size() >= int64(len(data)) {
		t.Errorf("thumbnail file size = %d bytes, want a nonzero size smaller than the %d-byte original", info.Size(), len(data))
	}

	// Re-running must be a no-op: the row already has a thumbnail reference,
	// so ListMissingThumbnail must not select it again.
	if err := run(logger); err != nil {
		t.Fatalf("second run() = %v, want nil (idempotent)", err)
	}
}

// seedBackfillUploader seeds an identity.household and an identity.member —
// the uploader every seeded photo/item/bin/location in this test attributes
// to — split out of the test body proper to keep its own cognitive
// complexity under the linter's threshold.
func seedBackfillUploader(ctx context.Context, t *testing.T, pool *pgxpool.Pool) *identity.User {
	t.Helper()
	householdID := identity.NewHouseholdID()
	const householdQ = `INSERT INTO identity.household (id, name) VALUES ($1, 'Test Household')`
	if _, err := pool.Exec(ctx, householdQ, householdID.String()); err != nil {
		t.Fatalf("seed household: %v", err)
	}
	uploader := &identity.User{
		ID: identity.NewUserID(), HouseholdID: householdID, DisplayName: "Backfill Test",
		Email:        "backfill-" + identity.NewUserID().String() + "@example.com",
		PasswordHash: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		Role:         identity.RoleAdult, Color: identity.ColorIndigo,
	}
	if err := identityadapter.NewUserRepository(pool).Create(ctx, uploader); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return uploader
}

// testJPEGBytes builds an 800x600 JPEG comfortably larger than the 400px
// default thumbnail box, so the generated thumbnail is a real, smaller
// re-encode rather than a same-size passthrough.
func testJPEGBytes(t *testing.T) []byte {
	t.Helper()
	const w, h = 800, 600
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 100, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	return buf.Bytes()
}
