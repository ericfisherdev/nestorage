package domain

import (
	"context"
	"io"
	"time"

	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// PutMeta carries the facts PhotoService has already established about an
// upload — ContentHash (sha256, hex), SizeBytes, and ContentType — before
// ever calling PhotoStore.Put. This is Sprint 5's reconciliation (R2):
// staging, sniffing, decode/dimension validation, and hashing all moved into
// PhotoService (behind PhotoValidator), so by the time Put runs, every fact
// it would otherwise have had to derive from the bytes themselves is already
// known. Put trusts PutMeta completely — it never re-sniffs or re-hashes.
type PutMeta struct {
	ContentHash string
	SizeBytes   int64
	ContentType string
	// Variant selects which key suffix Put derives (see
	// internal/media/adapter's buildStorageKey): PhotoVariantThumb appends
	// "_thumb" before the extension, landing the thumbnail on its own key
	// derived from the SAME ContentHash as the full image (NSTR-84). The
	// zero value behaves exactly like PhotoVariantFull, so every call site
	// that predates NSTR-84 (which never sets this field) keeps landing on
	// the unsuffixed key.
	Variant PhotoVariant
}

// PhotoStore persists and serves already-validated photo bytes behind a
// swappable port (LocalPhotoStore today; NSTR-35 adds an S3-backed
// adapter). Put streams r to storage, keyed by itemID and the content hash
// PutMeta carries — it builds the key and writes the bytes, nothing more
// (see PutMeta's own doc for why: every fact Put would otherwise have to
// derive from the bytes was already established by PhotoService upstream).
// Open streams the bytes back for serving; Delete removes them (idempotent
// on a missing ref); URL returns a locator for already-stored bytes.
//
// Put error contract: ErrUnsupportedMediaType when meta.ContentType is not
// an accepted image format. Open, Delete, and URL treat a missing ref
// according to their own docs below.
type PhotoStore interface {
	// Put streams r to storage under a key derived from itemID and
	// meta.ContentHash — items/<item>/<hash>.<ext> (see
	// internal/media/adapter's buildStorageKey) — and returns that key as
	// the StorageRef. The key is content-addressed and item-scoped: an
	// identical upload to the same item lands on the same key (dedup), and
	// the same bytes on two different items never collide, so there is no
	// cross-item refcounting to reason about (see PhotoRepository's own
	// doc). Returns ErrUnsupportedMediaType when meta.ContentType is not one
	// of the accepted image types (ContentTypeJPEG/ContentTypePNG).
	Put(ctx context.Context, itemID storagedomain.ItemID, meta PutMeta, r io.Reader) (StorageRef, error)

	// Open streams a stored photo's bytes back. It is deliberately
	// sequential (io.ReadCloser, NOT io.ReaderAt/io.Seeker): an S3 GetObject
	// body is sequential-only, and EXIF work (NSTR-36) happens on the
	// seekable staging file at upload time, never on a re-opened stored
	// object — so no PhotoStore.Open caller ever needs random access.
	// Returns ErrPhotoNotFound when ref is unknown.
	Open(ctx context.Context, ref StorageRef) (io.ReadCloser, error)

	// Delete removes ref's bytes. Idempotent: deleting an already-missing
	// ref is not an error (mirrors S3's DeleteObject semantics, which the
	// local adapter matches so PhotoService's delete pipeline behaves
	// identically regardless of backend).
	Delete(ctx context.Context, ref StorageRef) error

	// URL returns a locator for ref's stored bytes, or ErrPhotoNotFound when
	// ref is unknown. ttl bounds how long the locator stays valid; a
	// non-positive ttl asks the backend to apply its own configured
	// default. A backend that cannot expire what it returns (LocalPhotoStore
	// today) treats ttl as advisory.
	//
	// The two backends this port is designed for answer very differently:
	// an object-store adapter (NSTR-35) returns a presigned URL a client can
	// fetch directly. LocalPhotoStore has no such direct link — it confirms
	// ref resolves to a stored object and returns ref's own string as a
	// stable, non-navigable locator. See SupportsDirectURL for how a caller
	// decides which behavior to expect without knowing the concrete backend
	// type.
	URL(ctx context.Context, ref StorageRef, ttl time.Duration) (string, error)

	// SupportsDirectURL reports whether URL returns a browser-navigable
	// locator a caller may safely redirect a client to, as opposed to
	// LocalPhotoStore's non-navigable stable-string locator. PhotoService's
	// serving seam (DirectURL/SupportsDirectURL) reads this once, at
	// request time, to decide whether to redirect or stream through the Go
	// process — asking the store itself, rather than threading a
	// separately-configured "which backend" flag through every consumer,
	// means the two can never drift out of sync.
	SupportsDirectURL() bool
}

// StagedUpload is the outcome of PhotoValidator.ValidateAndStage: a temp
// file on disk (Path) holding the complete, validated upload, plus the
// content type and size PhotoService needs to build PutMeta. The caller
// owns Path and is responsible for removing it once done (both hashing it
// and handing it to PhotoStore.Put read from the same file); on a
// validation failure the adapter has already removed the staging file
// itself and the returned StagedUpload is the zero value.
type StagedUpload struct {
	Path        string
	ContentType string
	SizeBytes   int64
}

// PhotoValidator validates and stages an upload's bytes, enforcing the
// upload-acceptance rules every backend must guarantee regardless of where
// bytes ultimately land: content type sniffed from the bytes themselves
// (never a caller-declared claim), a hard cap on total bytes read, and a
// decode-plus-dimension cross-check against a decompression bomb. Injected
// into PhotoService the same shape NSTR-36 injects PhotoScrubber — both
// keep PhotoStore adapters validation/scrub-agnostic (Sprint 5
// reconciliation R1).
type PhotoValidator interface {
	// ValidateAndStage streams r into a new temp file, rejecting an upload
	// that is not JPEG or PNG (ErrUnsupportedMediaType), that decodes
	// implausibly or whose claimed dimensions exceed the decompression-bomb
	// guard (ErrInvalidPhoto), or that exceeds maxUploadBytes
	// (ErrPhotoTooLarge — nothing is written past the cap). On success the
	// caller owns the returned StagedUpload.Path and must remove it; on
	// failure the staging file has already been removed and the returned
	// StagedUpload is the zero value.
	ValidateAndStage(ctx context.Context, r io.Reader, maxUploadBytes int64) (StagedUpload, error)
}

// ItemPhoto pairs a Photo with its item-scoped ordering fields from the
// item_photo junction (00010_item_photo.sql) — PhotoRepository.ListByItem's
// element type, and PhotoService.ListForItem's.
type ItemPhoto struct {
	Photo     Photo
	Position  int
	IsPrimary bool
}

// PhotoRepository persists photo metadata (not the bytes) and the
// item_photo junction attaching a photo to the item it was uploaded for.
// Implementations live in the adapter package.
//
// Item-scoped dedup dissolves cross-item refcounting (Sprint 5
// reconciliation R6): because a photo's StorageRef embeds its owning item,
// one photo row is attached to exactly one item in practice, so Delete can
// safely remove the item_photo row, the photo row, and the stored object,
// in that order, with no reference count to check first. The
// item_photo.photo_id foreign key (ON DELETE RESTRICT) is the guard: were a
// second junction row ever to exist, it blocks the photo delete rather than
// silently orphaning another item's bytes.
type PhotoRepository interface {
	// Create inserts photo (not yet attached to any item — see
	// AttachToItem) and populates its CreatedAt. photo.ThumbnailRef, when
	// non-nil, is written in this SAME INSERT (NSTR-84) — there is no
	// separate write path on the upload flow; SetThumbnailRef exists only
	// for cmd/backfill-thumbs' later UPDATE against an existing row. Returns
	// ErrDuplicatePhoto when photo.StorageRef collides with an existing row
	// (the photo_storage_ref_uniq constraint), or identity.ErrUserNotFound when
	// photo.UploadedBy is unknown.
	Create(ctx context.Context, photo *Photo) error

	// GetForItem returns the photo, scoped to itemID via the item_photo
	// junction — the visibility-scoping fix (Sprint 5 reconciliation R5)
	// that stops a caller who can see one item from reading another,
	// invisible item's photo by guessing its id: returns ErrPhotoNotFound
	// both when id is unknown and when it exists but is not attached to
	// itemID. Every PhotoService method that reads or mutates one photo
	// calls this (never a bare, unscoped Get) after first confirming itemID
	// itself is visible to the viewer.
	GetForItem(ctx context.Context, itemID storagedomain.ItemID, id PhotoID) (*Photo, error)

	// FindByStorageRef returns the photo carrying ref, or ErrPhotoNotFound —
	// the expected "not a duplicate" outcome for a genuinely new upload, not
	// an exceptional one. Deliberately NOT item-scoped: PhotoService.Upload
	// calls this immediately after PhotoStore.Put, before any item_photo row
	// exists yet to scope through.
	FindByStorageRef(ctx context.Context, ref StorageRef) (*Photo, error)

	// AttachToItem inserts the item_photo row joining photoID to itemID at
	// position, marked primary or not. Returns storagedomain.ErrItemNotFound
	// when itemID is unknown, or ErrPhotoNotFound when photoID is unknown
	// (both mapped from the junction's foreign keys).
	AttachToItem(ctx context.Context, itemID storagedomain.ItemID, photoID PhotoID, position int, isPrimary bool) error

	// ListByItem returns itemID's attached photos ordered by position, or an
	// empty slice when none are attached. Not itself visibility-scoped —
	// PhotoService.ListForItem confirms itemID is visible to the viewer
	// first.
	ListByItem(ctx context.Context, itemID storagedomain.ItemID) ([]ItemPhoto, error)

	// Delete removes the (itemID, id) item_photo row and then id's photo
	// row, in that order (see the type doc). itemID scopes WHICH junction
	// row is removed — deliberately, not just an unscoped "every junction
	// row referencing id": that is what keeps the photo delete's foreign-key
	// guard meaningful. Were a second junction row (for some other item) to
	// still reference id when the photo row delete runs, item_photo_photo_id_fkey's
	// ON DELETE RESTRICT blocks it, and Delete returns a wrapped
	// ErrInvalidPhoto rather than silently orphaning that other item's
	// bytes. Returns ErrPhotoNotFound when id is unknown. Not itself
	// visibility-scoped: the caller (PhotoService.Delete) is responsible for
	// authorizing the delete via a preceding GetForItem, mirroring
	// storagedomain.ItemRepository.Delete's identical contract.
	Delete(ctx context.Context, itemID storagedomain.ItemID, id PhotoID) error

	// SetPrimary clears itemID's current primary photo (if any) and marks
	// photoID primary instead — a thin method over the item_photo_primary_uniq
	// partial unique index (at most one primary per item). Returns
	// ErrPhotoNotFound when photoID is not attached to itemID.
	SetPrimary(ctx context.Context, itemID storagedomain.ItemID, photoID PhotoID) error

	// Reorder rewrites itemID's attached photos' positions to match order's
	// sequence (order[i] becomes position i). order must list every photo
	// currently attached to itemID exactly once; a partial or foreign list
	// leaves those rows' positions unchanged relative to items order omits,
	// which callers must not rely on — PhotoService.Reorder always builds
	// order from a preceding ListForItem.
	Reorder(ctx context.Context, itemID storagedomain.ItemID, order []PhotoID) error

	// SetThumbnailRef records ref as photoID's thumbnail variant. Used only
	// by cmd/backfill-thumbs (NSTR-84): the upload path instead populates
	// Photo.ThumbnailRef before Create, in the same INSERT (see Create's own
	// doc) — this method exists for the separate UPDATE a backfill run makes
	// against an already-existing row. Returns ErrPhotoNotFound when photoID
	// is unknown.
	SetThumbnailRef(ctx context.Context, photoID PhotoID, ref StorageRef) error

	// ListMissingThumbnail returns up to limit photo rows whose thumbnail
	// reference is still NULL, ordered by id ascending and starting strictly
	// after afterID (the zero PhotoID for the first page) — a resumable,
	// deterministic cursor cmd/backfill-thumbs (NSTR-84) re-runs until it
	// gets back an empty slice. Each row carries just enough (its own id,
	// the item it is attached to, its full StorageRef, and its content
	// type) for the backfill to generate and store a thumbnail without a
	// second query per row.
	ListMissingThumbnail(ctx context.Context, limit int, afterID PhotoID) ([]MissingThumbnailPhoto, error)

	// ListPrimaryForItems returns each item in itemIDs' primary photo id,
	// keyed by item id — an item with no primary photo (or no photos at
	// all) is simply absent from the map, never an error. This is the
	// media-side half of the cross-context fan-in read Sprint 5
	// reconciliation R8 assigns to NSTR-37: PhotoService.ListPrimaryThumbRefs
	// calls this once per list render (never once per item) to back
	// storage's own primaryPhotoRefLister port. Not itself
	// visibility-scoped — see ListPrimaryThumbRefs's own doc for why
	// itemIDs is trusted as already caller-scoped, mirroring ListByItem's
	// identical "caller already scoped it" contract.
	ListPrimaryForItems(ctx context.Context, itemIDs []storagedomain.ItemID) (map[storagedomain.ItemID]PhotoID, error)
}

// MissingThumbnailPhoto is one row PhotoRepository.ListMissingThumbnail
// returns. ContentHash lets cmd/backfill-thumbs key the generated
// thumbnail's Put under the SAME content hash as the full image (see
// PutMeta.Variant/buildStorageKey), exactly like the upload path, without a
// second query to recover it.
type MissingThumbnailPhoto struct {
	ID          PhotoID
	ItemID      storagedomain.ItemID
	StorageRef  StorageRef
	ContentHash string
	ContentType string
}
