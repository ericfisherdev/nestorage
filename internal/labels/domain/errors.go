package domain

import (
	"errors"
	"fmt"
)

// Domain errors for the labels bounded context. One context, one error file,
// matching storage/domain/errors.go's own convention.
var (
	// ErrInvalidLabelSize is returned (wrapped, naming the offending
	// field/axis) by LabelSize.Validate for a malformed size, and by
	// Registry.NewRegistry when a builtin entry fails that same check.
	ErrInvalidLabelSize = errors.New("labels: invalid label size")

	// ErrUnknownLabelSize is returned by Registry.Get for an id not present
	// in the registry.
	ErrUnknownLabelSize = errors.New("labels: unknown label size")

	// ErrNoLabels is returned by LabelRenderer.Render when given an empty
	// labels slice — the documented port contract every implementation
	// (NSTR-49's PDF adapter, and any later thermal-printer adapter) must
	// honor.
	ErrNoLabels = errors.New("labels: no labels to render")

	// ErrInvalidStartOffset is returned (wrapped, naming the offending
	// value) by LabelRenderer.Render when startOffset falls outside
	// 0..size.CellsPerPage()-1 — the same documented port contract
	// ErrNoLabels enforces.
	ErrInvalidStartOffset = errors.New("labels: invalid start offset")

	// ErrEmptyBatch is returned by app.BatchService.RenderLocationBatch when
	// a location has no bins viewer may see — printing an empty sheet would
	// be a silent no-op, so this is surfaced as an error the web handler
	// turns into an actionable message (NSTR-50) rather than a blank
	// download.
	ErrEmptyBatch = errors.New("labels: batch has no visible bins to print")

	// ErrBatchTooLarge is returned (as a *BatchTooLargeError, so a caller
	// can recover the actual count and MaxBatchLabels via errors.As) by
	// app.BatchService.RenderLocationBatch when a location holds more
	// visible bins than MaxBatchLabels — NSTR-50's print-batch cap.
	ErrBatchTooLarge = errors.New("labels: batch exceeds the maximum label count")
)

// MaxBatchLabels bounds a single app.BatchService.RenderLocationBatch call
// to at most this many labels — ten full 30-up sheets (Avery 5160). The cap
// exists to bound render time and memory on the appliance: PDFSheetRenderer
// builds its entire document in memory before returning bytes (see its own
// doc), and an unbounded location could otherwise make a single request
// spend an unbounded amount of both. This is a domain rule, not handler
// trivia, so the future JSON API (NSTR Sprint 8) inherits it automatically
// rather than needing its own copy.
const MaxBatchLabels = 300

// BatchTooLargeError decorates ErrBatchTooLarge with the batch's actual bin
// Count and the Max it was measured against — the two numbers the labels
// web handler (NSTR-50) needs to compose an actionable "this location has N
// bins; batches are limited to 300" response, which a bare sentinel cannot
// carry. errors.Is(err, ErrBatchTooLarge) still matches via Unwrap, so a
// caller that only checks the sentinel is unaffected.
type BatchTooLargeError struct {
	Count int
	Max   int
}

// Error implements the error interface.
func (e *BatchTooLargeError) Error() string {
	return fmt.Sprintf("%s: %d bins exceeds the %d-label limit", ErrBatchTooLarge, e.Count, e.Max)
}

// Unwrap exposes ErrBatchTooLarge to errors.Is.
func (e *BatchTooLargeError) Unwrap() error {
	return ErrBatchTooLarge
}
