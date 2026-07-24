package adapter_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"

	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

// RunPhotoStoreConformance exercises the domain.PhotoStore port contract
// against newStore's result: byte-identical read-back, the exact
// items/<item>/<hash>.<ext> key layout, item-scoped dedup to one object for
// identical bytes, and idempotent Delete/not-found Open on a missing key.
// NSTR-35 reruns this exact suite against its S3 adapter — it therefore
// asserts storage behavior only (Sprint 5 reconciliation R2): the
// extension-lie and over-limit rejections now live in
// TestPhotoValidator_* (photo_validation_test.go) and
// TestPhotoService_* instead, since PhotoStore.Put no longer validates
// anything itself.
func RunPhotoStoreConformance(t *testing.T, newStore func(t *testing.T) domain.PhotoStore) {
	t.Helper()

	t.Run("PutAndOpenRoundTripByteIdentical", func(t *testing.T) {
		store := newStore(t)
		itemID := storagedomain.NewItemID()
		data := jpegBytes(t)
		meta := computeMeta(data, domain.ContentTypeJPEG)

		ref, err := store.Put(context.Background(), itemID, meta, bytes.NewReader(data))
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
		rc, err := store.Open(context.Background(), ref)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		got, readErr := io.ReadAll(rc)
		_ = rc.Close()
		if readErr != nil {
			t.Fatalf("read opened photo: %v", readErr)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("Open returned %d bytes, want %d byte-identical to the upload", len(got), len(data))
		}
	})

	t.Run("KeyLayoutIsItemsItemHashExt", func(t *testing.T) {
		store := newStore(t)
		itemID := storagedomain.NewItemID()
		data := pngBytes(t)
		meta := computeMeta(data, domain.ContentTypePNG)

		ref, err := store.Put(context.Background(), itemID, meta, bytes.NewReader(data))
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
		want := "items/" + itemID.String() + "/" + meta.ContentHash + ".png"
		if ref.String() != want {
			t.Fatalf("Put ref = %q, want %q", ref.String(), want)
		}
	})

	t.Run("DedupSameItemSameBytesOneObject", func(t *testing.T) {
		store := newStore(t)
		itemID := storagedomain.NewItemID()
		data := jpegBytes(t)
		meta := computeMeta(data, domain.ContentTypeJPEG)

		ref1, err := store.Put(context.Background(), itemID, meta, bytes.NewReader(data))
		if err != nil {
			t.Fatalf("first Put: %v", err)
		}
		ref2, err := store.Put(context.Background(), itemID, meta, bytes.NewReader(data))
		if err != nil {
			t.Fatalf("second Put: %v", err)
		}
		if ref1 != ref2 {
			t.Fatalf("identical bytes to the same item produced different refs: %s vs %s", ref1, ref2)
		}
	})

	t.Run("DeleteIdempotentOnMissingKey", func(t *testing.T) {
		store := newStore(t)
		if err := store.Delete(context.Background(), domain.StorageRef("items/nope/deadbeef.jpg")); err != nil {
			t.Fatalf("Delete(missing) = %v, want nil (idempotent)", err)
		}
	})

	t.Run("DeleteExistingObjectThenNotFound", func(t *testing.T) {
		store := newStore(t)
		itemID := storagedomain.NewItemID()
		data := jpegBytes(t)
		meta := computeMeta(data, domain.ContentTypeJPEG)

		ref, err := store.Put(context.Background(), itemID, meta, bytes.NewReader(data))
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := store.Delete(context.Background(), ref); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := store.Open(context.Background(), ref); !errors.Is(err, domain.ErrPhotoNotFound) {
			t.Fatalf("Open after Delete = %v, want ErrPhotoNotFound", err)
		}
		// Deleting it again is still not an error (idempotent).
		if err := store.Delete(context.Background(), ref); err != nil {
			t.Fatalf("second Delete = %v, want nil (idempotent)", err)
		}
	})

	t.Run("OpenUnknownRefNotFound", func(t *testing.T) {
		store := newStore(t)
		if _, err := store.Open(context.Background(), domain.StorageRef("items/nope/deadbeef.jpg")); !errors.Is(err, domain.ErrPhotoNotFound) {
			t.Fatalf("Open(unknown) = %v, want ErrPhotoNotFound", err)
		}
	})
}
