package domain

import "fmt"

// Builtin label size ids — Avery's own template numbers where one exists,
// so a household matches a size to the sheet they actually bought.
const (
	LabelSizeAvery5160 LabelSizeID = "avery-5160"
	LabelSizeAvery5163 LabelSizeID = "avery-5163"
	LabelSizeSingle4x6 LabelSizeID = "single-4x6"
)

// builtinLabelSizes lists every size Registry ships with, in the fixed
// declaration order NewRegistry preserves for List — NSTR-51's size
// dropdown depends on that order staying stable across renders.
var builtinLabelSizes = []LabelSize{
	{
		// Avery 5160: US Letter, 30-up (3 columns x 10 rows), 1in x 2-5/8in
		// cells — the address-label template most home label printers
		// already stock.
		ID:             LabelSizeAvery5160,
		DisplayName:    "Avery 5160 (30-up, 1\" x 2-5/8\")",
		PageWidthMM:    215.9,
		PageHeightMM:   279.4,
		MarginTopMM:    12.7,
		MarginBottomMM: 12.7,
		MarginLeftMM:   4.7625,
		MarginRightMM:  4.7625,
		Columns:        3,
		Rows:           10,
		CellWidthMM:    66.675,
		CellHeightMM:   25.4,
		GutterXMM:      3.175,
		GutterYMM:      0,
	},
	{
		// Avery 5163: US Letter, 10-up (2 columns x 5 rows), 2in x 4in
		// cells — a larger shipping-label template, roomy enough for a QR
		// code plus the bin's name and location.
		ID:             LabelSizeAvery5163,
		DisplayName:    "Avery 5163 (10-up, 2\" x 4\")",
		PageWidthMM:    215.9,
		PageHeightMM:   279.4,
		MarginTopMM:    12.7,
		MarginBottomMM: 12.7,
		MarginLeftMM:   3.96875,
		MarginRightMM:  3.96875,
		Columns:        2,
		Rows:           5,
		CellWidthMM:    101.6,
		CellHeightMM:   50.8,
		GutterXMM:      4.7625,
		GutterYMM:      0,
	},
	{
		// single-4x6: one 4in x 6in label per page, page and cell equal —
		// a standalone thermal/photo-paper label with no sheet grid at
		// all, the degenerate 1x1 case CellsPerPage's math still covers
		// without a special case.
		ID:             LabelSizeSingle4x6,
		DisplayName:    "Single 4\" x 6\"",
		PageWidthMM:    101.6,
		PageHeightMM:   152.4,
		MarginTopMM:    0,
		MarginBottomMM: 0,
		MarginLeftMM:   0,
		MarginRightMM:  0,
		Columns:        1,
		Rows:           1,
		CellWidthMM:    101.6,
		CellHeightMM:   152.4,
		GutterXMM:      0,
		GutterYMM:      0,
	},
}

// Registry is the catalogue of supported LabelSize geometries. Every
// instance it hands back through Get/List has already passed Validate — the
// fail-fast check NewRegistry runs once, at construction, so an invalid
// builtin fails process boot (cmd/server's composition root constructs the
// registry once, at startup) rather than surfacing on a household's first
// print request. Adding a new size (AC) is purely a matter of appending to
// builtinLabelSizes — no other file in this package changes.
type Registry struct {
	sizes []LabelSize
	byID  map[LabelSizeID]LabelSize
}

// NewRegistry constructs a Registry from builtinLabelSizes, validating every
// entry (wrapped ErrInvalidLabelSize on the first failure) and rejecting a
// duplicate id (also wrapped ErrInvalidLabelSize) before any instance is
// exposed to a caller.
func NewRegistry() (*Registry, error) {
	return newRegistryFrom(builtinLabelSizes)
}

// newRegistryFrom is NewRegistry's actual construction logic, factored out
// so registry_internal_test.go can exercise the duplicate-id rejection path
// directly against a deliberately malformed slice — builtinLabelSizes itself
// carries no duplicate, so that failure path is otherwise unreachable from a
// black-box test.
func newRegistryFrom(candidates []LabelSize) (*Registry, error) {
	byID := make(map[LabelSizeID]LabelSize, len(candidates))
	for _, size := range candidates {
		if err := size.Validate(); err != nil {
			return nil, fmt.Errorf("labels: registry entry %q: %w", size.ID, err)
		}
		if _, exists := byID[size.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate id %q", ErrInvalidLabelSize, size.ID)
		}
		byID[size.ID] = size
	}
	return &Registry{sizes: candidates, byID: byID}, nil
}

// Get returns the LabelSize registered under id, or ErrUnknownLabelSize when
// no entry matches.
func (r *Registry) Get(id LabelSizeID) (LabelSize, error) {
	size, ok := r.byID[id]
	if !ok {
		return LabelSize{}, fmt.Errorf("%w: %q", ErrUnknownLabelSize, id)
	}
	return size, nil
}

// List returns every registered LabelSize in fixed declaration order
// (builtinLabelSizes' own order) — stable across calls and across process
// restarts, which is what makes NSTR-51's size dropdown render identically
// every time.
func (r *Registry) List() []LabelSize {
	sizes := make([]LabelSize, len(r.sizes))
	copy(sizes, r.sizes)
	return sizes
}
