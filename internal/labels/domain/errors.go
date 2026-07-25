package domain

import "errors"

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
)
