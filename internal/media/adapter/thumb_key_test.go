package adapter_test

import (
	"bytes"
	"context"
	"testing"

	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"

	"github.com/ericfisherdev/nestorage/internal/media/adapter"
	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

// TestBuildStorageKey_ThumbVariantSuffix proves the variant-aware key layout
// (NSTR-84) buildStorageKey builds — asserted through LocalPhotoStore.Put
// (buildStorageKey itself is unexported), mirroring
// conformance_test.go's KeyLayoutIsItemsItemHashExt style. The full and
// thumb keys for the SAME content hash differ in exactly the "_thumb"
// segment before the extension — everything else (the items/<item> prefix,
// the hash, the extension) is identical, which is what lets item-scoped
// dedup keep working unchanged for both variants.
func TestBuildStorageKey_ThumbVariantSuffix(t *testing.T) {
	store, err := adapter.NewLocalPhotoStore(t.TempDir(), 10<<20)
	if err != nil {
		t.Fatalf("NewLocalPhotoStore: %v", err)
	}
	itemID := storagedomain.NewItemID()
	data := jpegBytes(t)
	fullMeta := computeMeta(data, domain.ContentTypeJPEG)
	thumbMeta := fullMeta
	thumbMeta.Variant = domain.PhotoVariantThumb

	fullRef, err := store.Put(context.Background(), itemID, fullMeta, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put(full): %v", err)
	}
	thumbRef, err := store.Put(context.Background(), itemID, thumbMeta, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put(thumb): %v", err)
	}

	wantFull := "items/" + itemID.String() + "/" + fullMeta.ContentHash + ".jpg"
	wantThumb := "items/" + itemID.String() + "/" + fullMeta.ContentHash + "_thumb.jpg"
	if fullRef.String() != wantFull {
		t.Errorf("full ref = %q, want %q", fullRef.String(), wantFull)
	}
	if thumbRef.String() != wantThumb {
		t.Errorf("thumb ref = %q, want %q", thumbRef.String(), wantThumb)
	}
	if fullRef == thumbRef {
		t.Fatal("full and thumb refs must differ")
	}
}

// TestBuildStorageKey_FullVariantIsZeroValueDefault proves PutMeta's zero
// Variant behaves exactly like domain.PhotoVariantFull — every Put call site
// that predates NSTR-84 never sets Variant and must keep landing on the
// unsuffixed key.
func TestBuildStorageKey_FullVariantIsZeroValueDefault(t *testing.T) {
	store, err := adapter.NewLocalPhotoStore(t.TempDir(), 10<<20)
	if err != nil {
		t.Fatalf("NewLocalPhotoStore: %v", err)
	}
	itemID := storagedomain.NewItemID()
	data := jpegBytes(t)
	meta := computeMeta(data, domain.ContentTypeJPEG) // Variant left at its zero value

	ref, err := store.Put(context.Background(), itemID, meta, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	want := "items/" + itemID.String() + "/" + meta.ContentHash + ".jpg"
	if ref.String() != want {
		t.Errorf("ref = %q, want %q (zero Variant behaves as full)", ref.String(), want)
	}
}
