package domain

import "context"

// PhotoScrubber strips privacy-sensitive metadata (EXIF GPS coordinates,
// camera serials, timestamps, ...) from an already-validated upload's bytes,
// baking in any orientation correction needed to keep the photo visually
// upright before that orientation tag is discarded. NSTR-36 exists because a
// photo taken indoors carries the GPS coordinates of the user's home;
// letting that reach PhotoStore (and, from there, any backup) would leak the
// household's location.
//
// PhotoService injects PhotoScrubber the same shape it injects
// PhotoValidator (see PhotoValidator's own doc) — both keep PhotoStore
// adapters agnostic of validation/scrubbing (Sprint 5 reconciliation R1).
// Scrub runs between ValidateAndStage and the content hash PutMeta carries:
// PhotoService hashes Scrub's OUTPUT, never the raw upload (Sprint 5
// reconciliation R3), so the storage key and the persisted Photo row always
// describe the scrubbed bytes.
type PhotoScrubber interface {
	// Scrub strips metadata from data — already known, via contentType, to
	// be one of the accepted image types (ContentTypeJPEG/ContentTypePNG) —
	// and returns the scrubbed bytes. ctx is accepted for the same reason
	// PhotoValidator.ValidateAndStage accepts one (consistent port shape
	// across this context's upload pipeline), even though today's adapter
	// does no I/O that could block on it.
	//
	// Returns a wrapped ErrInvalidPhoto for data that fails to decode, whose
	// structure a hardened parse rejects, or whose claimed dimensions trip
	// the decompression-bomb guard — Scrub never passes unexamined bytes
	// through on a rejection. contentType outside the accepted set also
	// returns ErrInvalidPhoto (defensive: PhotoValidator's sniff should have
	// rejected it already).
	Scrub(ctx context.Context, data []byte, contentType string) ([]byte, error)
}
