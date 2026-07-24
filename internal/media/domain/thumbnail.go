package domain

import (
	"fmt"
	"strings"
)

// PhotoVariant selects which size of a photo's bytes a caller wants: the
// original upload (PhotoVariantFull) or the generated thumbnail
// (PhotoVariantThumb, NSTR-84). A typed enum rather than a bool, matching
// the house pattern (see StorageBackend's identical rationale), so the serve
// path and the storage key both read as intent.
type PhotoVariant string

// PhotoVariantFull is the original, full-resolution upload. PhotoVariantThumb
// is the bounded-size variant PhotoThumbnailer generates.
const (
	PhotoVariantFull  PhotoVariant = "full"
	PhotoVariantThumb PhotoVariant = "thumb"
)

// Valid reports whether v is a known PhotoVariant.
func (v PhotoVariant) Valid() bool {
	switch v {
	case PhotoVariantFull, PhotoVariantThumb:
		return true
	default:
		return false
	}
}

// String returns v's own spelling.
func (v PhotoVariant) String() string { return string(v) }

// ParsePhotoVariant parses a caller-supplied variant (e.g. NSTR-37's `size`
// query parameter), rejecting anything unknown rather than trusting the
// caller blindly — mirrors ParseStorageBackend's identical shape, including
// the explicit default switch case.
func ParsePhotoVariant(s string) (PhotoVariant, error) {
	v := PhotoVariant(strings.ToLower(strings.TrimSpace(s)))
	switch v {
	case PhotoVariantFull, PhotoVariantThumb:
		return v, nil
	default:
		return "", fmt.Errorf("media: unknown photo variant %q", s)
	}
}

// ThumbResult is PhotoThumbnailer.Thumbnail's outcome: the encoded thumbnail
// bytes, the content type it was encoded as (always equal to the input
// content type — see PhotoThumbnailer's own doc), and the pixel dimensions
// it was scaled to.
type ThumbResult struct {
	Bytes       []byte
	ContentType string
	Width       int
	Height      int
}

// PhotoThumbnailer generates a bounded-size thumbnail variant from an
// already-validated, already-scrubbed photo's bytes (NSTR-84). It mirrors
// PhotoScrubber's shape (a single method, the same signature style) so the
// upload pipeline reads uniformly: stage+validate -> scrub -> thumbnail ->
// hash -> Put.
//
// The thumbnail geometry rule is a domain-level contract, not an adapter
// detail: the variant fits within an implementation-configured bounding box
// on its long edge, aspect ratio preserved, and is never upscaled — an image
// already smaller than the box is re-encoded at its own size rather than
// skipped, so every photo ends up with a thumb object and the serve path
// never needs a small-original special case.
type PhotoThumbnailer interface {
	// Thumbnail generates data's thumbnail variant — already known, via
	// contentType, to be one of the accepted image types
	// (ContentTypeJPEG/ContentTypePNG). The returned ThumbResult.ContentType
	// always equals contentType (see the type's own doc), so a caller never
	// needs variant-specific Content-Type logic.
	//
	// Returns a wrapped ErrInvalidPhoto when data does not decode or whose
	// claimed dimensions trip the decompression-bomb guard, or
	// ErrUnsupportedMediaType for any other contentType (defensive:
	// PhotoService only ever calls this with an already-accepted type).
	Thumbnail(data []byte, contentType string) (ThumbResult, error)
}
