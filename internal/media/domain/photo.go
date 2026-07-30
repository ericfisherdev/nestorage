package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// Accepted image content types — the upload accept-list, and the single
// source of truth both Photo.Validate and the adapter's
// content-type-to-extension mapping key off. image/webp is deliberately NOT
// accepted (Sprint 5 reconciliation R3): NSTR-36's scrubber handles only
// JPEG and PNG, and Go's standard library has no webp encoder, so a scrubbed
// webp could never be re-encoded — accepting it at the sniff step would let
// an upload pass validation and then fail scrubbing.
const (
	ContentTypeJPEG = "image/jpeg"
	ContentTypePNG  = "image/png"
)

// acceptedContentTypes is the set Photo.Validate checks ContentType against.
var acceptedContentTypes = map[string]struct{}{
	ContentTypeJPEG: {},
	ContentTypePNG:  {},
}

// contentHashPattern matches a hex-encoded sha256 sum: exactly 64 lowercase
// hex characters, the shape every PutMeta.ContentHash PhotoService computes
// always has.
var contentHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Photo domain errors.
var (
	// ErrPhotoNotFound is returned by PhotoRepository/PhotoStore methods that
	// look up or mutate a specific photo when no matching row/object exists,
	// or (PhotoRepository.GetForItem) when the photo exists but is not
	// attached to the item the caller scoped the lookup to.
	ErrPhotoNotFound = errors.New("media: photo not found")
	// ErrInvalidPhoto is returned by Photo.Validate for a malformed photo,
	// and by the adapter for bytes that fail the storage-ref-escapes-root
	// guard or an unexpected database CHECK violation.
	ErrInvalidPhoto = errors.New("media: invalid photo")
	// ErrUnsupportedMediaType is returned when an upload's sniffed content
	// type is not an accepted image format. The web layer maps it to 415.
	ErrUnsupportedMediaType = errors.New("media: unsupported media type")
	// ErrPhotoTooLarge is returned when an upload exceeds the configured
	// MEDIA_MAX_UPLOAD_BYTES limit, before anything is written to storage.
	// The web layer maps it to 413.
	ErrPhotoTooLarge = errors.New("media: photo exceeds the maximum size")
	// ErrDuplicatePhoto is returned by PhotoRepository.Create when a photo
	// with the same storage ref already exists (the photo_storage_ref_uniq
	// constraint, 00010_item_photo.sql). PhotoService resolves it by
	// fetching and returning the existing photo instead of surfacing an
	// error, so callers never see it directly (the item-scoped-dedup race
	// two concurrent uploads of the same bytes to the same item can hit).
	ErrDuplicatePhoto = errors.New("media: duplicate photo content")
)

// StorageRef is an opaque key identifying a photo's bytes in the PhotoStore:
// the PutMeta-derived, content-addressed, item-scoped key
// (items/<item>/<hash>.<ext>) every PhotoStore backend builds identically
// (see PhotoStore's own doc). The bytes themselves are never stored in the
// database — only this reference is.
type StorageRef string

// String returns the ref's string value.
func (r StorageRef) String() string { return string(r) }

// Photo is one stored image, attached to exactly one storage item via
// PhotoRepository.AttachToItem/ListByItem/GetForItem (see the item_photo
// junction). StorageRef is the PhotoStore key; ContentHash is the hex sha256
// of the stored bytes (recorded server-side, never from a caller claim) —
// SizeBytes and ContentType are the other server-verified upload facts.
// StorageBackend records which PhotoStore implementation wrote the bytes
// (NSTR-35's mixed-state requirement); UploadedBy is the user who uploaded
// it, for item history attribution.
type Photo struct {
	ID          PhotoID
	HouseholdID identity.HouseholdID
	StorageRef  StorageRef
	// ThumbnailRef is the thumbnail variant's StorageRef (NSTR-84), or nil
	// when no thumbnail exists — either generation/storage failed at upload
	// time (PhotoService.Upload treats that as non-fatal, see its own doc)
	// or the photo predates NSTR-84 and has not been backfilled yet. A
	// pointer, not an empty StorageRef: StorageRef("") is already rejected
	// by Validate for StorageRef itself, so a pointer is the honest way to
	// express "no thumbnail" without inventing a sentinel value.
	ThumbnailRef   *StorageRef
	ContentHash    string
	SizeBytes      int64
	ContentType    string
	StorageBackend StorageBackend
	UploadedBy     identity.UserID
	CreatedAt      time.Time
}

// Validate reports whether the photo is well-formed, wrapping
// ErrInvalidPhoto. Every field is required in its canonical
// server-verified shape because every Photo PhotoService.Upload builds has
// them (PhotoService always computes ContentHash/SizeBytes/ContentType/
// StorageRef before constructing one) — a violation here signals an
// application-layer bug, not a legitimate edge case.
func (p Photo) Validate() error {
	if strings.TrimSpace(p.StorageRef.String()) == "" {
		return fmt.Errorf("%w: storage ref must not be blank", ErrInvalidPhoto)
	}
	if p.ThumbnailRef != nil && strings.TrimSpace(p.ThumbnailRef.String()) == "" {
		return fmt.Errorf("%w: thumbnail ref must not be blank when present", ErrInvalidPhoto)
	}
	if !contentHashPattern.MatchString(p.ContentHash) {
		return fmt.Errorf("%w: content hash must be a 64-character lowercase hex sha256, got %q", ErrInvalidPhoto, p.ContentHash)
	}
	if p.SizeBytes <= 0 {
		return fmt.Errorf("%w: size bytes must be positive, got %d", ErrInvalidPhoto, p.SizeBytes)
	}
	if _, ok := acceptedContentTypes[p.ContentType]; !ok {
		return fmt.Errorf("%w: content type %q is not accepted", ErrInvalidPhoto, p.ContentType)
	}
	if !p.StorageBackend.Valid() {
		return fmt.Errorf("%w: storage backend %q is not valid", ErrInvalidPhoto, p.StorageBackend)
	}
	return nil
}
