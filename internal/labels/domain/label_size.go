package domain

import (
	"fmt"
	"strings"
)

// LabelSizeID is the stable lookup identifier a LabelSize is registered and
// retrieved under (Registry.Get, Registry.List) — never the display name,
// which is free to change without breaking a persisted or bookmarked
// selection.
type LabelSizeID string

// String returns id's own spelling.
func (id LabelSizeID) String() string { return string(id) }

// overflowEpsilonMM is the tolerance LabelSize.Validate's overflow checks
// compare against, rather than a bare "<=": Registry's builtin geometries
// are derived from inch measurements (Avery's own templates), so converting
// them to millimetres leaves float64 sums a few thousandths of a millimetre
// over an exact page dimension — well inside what a print driver treats as
// identical, but enough to trip a naive floating-point comparison.
const overflowEpsilonMM = 0.01

// LabelSize describes one printable label sheet geometry: the physical page
// it prints onto, the grid of label cells on it, and the margins/gutters
// that position that grid on the page. Every dimension is in millimetres
// (float64) — gopdf's own Config takes an mm Unit directly (NSTR-49), so
// this geometry drops into its adapter with no unit conversion.
//
// LabelSize is a plain struct, like storage's Location/Bin: no logic beyond
// Validate and CellsPerPage lives on it. Registry is the only place
// instances are constructed in practice (see its own doc), and Validate
// runs there before any instance is exposed to a caller.
type LabelSize struct {
	ID          LabelSizeID
	DisplayName string

	// PageWidthMM/PageHeightMM are the physical sheet's own dimensions —
	// for single-4x6 these equal the single cell's own CellWidthMM/
	// CellHeightMM exactly (see Registry's own doc).
	PageWidthMM  float64
	PageHeightMM float64

	// MarginTopMM/MarginBottomMM/MarginLeftMM/MarginRightMM bound the
	// printable grid within PageWidthMM/PageHeightMM.
	MarginTopMM    float64
	MarginBottomMM float64
	MarginLeftMM   float64
	MarginRightMM  float64

	// Columns and Rows are the label grid's shape — CellsPerPage's two
	// factors.
	Columns int
	Rows    int

	// CellWidthMM/CellHeightMM are one label cell's own dimensions.
	CellWidthMM  float64
	CellHeightMM float64

	// GutterXMM/GutterYMM are the spacing between adjacent columns/rows —
	// zero for a size whose cells sit flush against each other (every
	// builtin's row gutter, and single-4x6's column gutter too, since
	// there is only the one cell).
	GutterXMM float64
	GutterYMM float64
}

// CellsPerPage returns the label grid's per-sheet capacity (Columns x
// Rows) — the pagination primitive NSTR-49's renderer pages a label set on,
// and the batch-cap/start-offset validation primitive NSTR-50's app-layer
// service and this package's own LabelRenderer contract build on.
func (s LabelSize) CellsPerPage() int {
	return s.Columns * s.Rows
}

// Validate reports whether s is well-formed, wrapping ErrInvalidLabelSize
// and naming the offending field/axis, matching Bin.Validate's own wrapping
// style. It checks: a non-blank ID, positive page and cell dimensions,
// Columns/Rows at least 1, non-negative margins and gutters, and that the
// label grid actually fits the page in both axes — the last of these
// compared with overflowEpsilonMM's tolerance rather than a bare "<=" (see
// its own doc for why).
func (s LabelSize) Validate() error {
	if strings.TrimSpace(string(s.ID)) == "" {
		return fmt.Errorf("%w: id must not be blank", ErrInvalidLabelSize)
	}
	if s.PageWidthMM <= 0 {
		return fmt.Errorf("%w: page width must be positive", ErrInvalidLabelSize)
	}
	if s.PageHeightMM <= 0 {
		return fmt.Errorf("%w: page height must be positive", ErrInvalidLabelSize)
	}
	if s.CellWidthMM <= 0 {
		return fmt.Errorf("%w: cell width must be positive", ErrInvalidLabelSize)
	}
	if s.CellHeightMM <= 0 {
		return fmt.Errorf("%w: cell height must be positive", ErrInvalidLabelSize)
	}
	if s.Columns < 1 {
		return fmt.Errorf("%w: columns must be at least 1", ErrInvalidLabelSize)
	}
	if s.Rows < 1 {
		return fmt.Errorf("%w: rows must be at least 1", ErrInvalidLabelSize)
	}
	if s.MarginTopMM < 0 {
		return fmt.Errorf("%w: top margin must not be negative", ErrInvalidLabelSize)
	}
	if s.MarginBottomMM < 0 {
		return fmt.Errorf("%w: bottom margin must not be negative", ErrInvalidLabelSize)
	}
	if s.MarginLeftMM < 0 {
		return fmt.Errorf("%w: left margin must not be negative", ErrInvalidLabelSize)
	}
	if s.MarginRightMM < 0 {
		return fmt.Errorf("%w: right margin must not be negative", ErrInvalidLabelSize)
	}
	if s.GutterXMM < 0 {
		return fmt.Errorf("%w: column gutter must not be negative", ErrInvalidLabelSize)
	}
	if s.GutterYMM < 0 {
		return fmt.Errorf("%w: row gutter must not be negative", ErrInvalidLabelSize)
	}

	width := s.MarginLeftMM + float64(s.Columns)*s.CellWidthMM + float64(s.Columns-1)*s.GutterXMM + s.MarginRightMM
	if width > s.PageWidthMM+overflowEpsilonMM {
		return fmt.Errorf("%w: columns/margins/gutters (%.4gmm) exceed page width (%.4gmm)", ErrInvalidLabelSize, width, s.PageWidthMM)
	}
	height := s.MarginTopMM + float64(s.Rows)*s.CellHeightMM + float64(s.Rows-1)*s.GutterYMM + s.MarginBottomMM
	if height > s.PageHeightMM+overflowEpsilonMM {
		return fmt.Errorf("%w: rows/margins/gutters (%.4gmm) exceed page height (%.4gmm)", ErrInvalidLabelSize, height, s.PageHeightMM)
	}
	return nil
}
