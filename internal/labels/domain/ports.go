package domain

import "context"

// BinLabel is one bin's fully pre-resolved label data — everything a
// renderer needs to draw one cell, and nothing it would have to look up
// itself. Deliberately NOT storagedomain.Bin: a bin's location display name
// needs a LocationRepository lookup, and QRPayload (NSTR-48) is built from
// the running app's own base URL, neither of which a renderer has any
// business reaching for. NSTR-50's app-layer batch service maps visible
// bins, their locations, and NSTR-48's payload builder into BinLabel values
// before ever calling LabelRenderer.Render.
type BinLabel struct {
	Code         string
	Name         string
	LocationName string
	QRPayload    string
}

// Document is a rendered label sheet's bytes, format-neutral: ContentType
// names what Data actually is (e.g. "application/pdf" for NSTR-49's gopdf
// adapter), so a later thermal-printer adapter can return "image/png" or raw
// ZPL through this same port with no change to LabelRenderer's signature or
// any caller of it.
type Document struct {
	ContentType string
	Data        []byte
}

// LabelRenderer is the outbound port for turning a set of bins into a
// printable label document. Implementations live in a sibling adapter
// package (NSTR-49's gopdf-backed adapter first; a thermal-printer adapter
// later, purely additive — see this package's own doc). No PDF-specific
// type appears anywhere in this signature.
//
// Render lays labels out on size's grid, one BinLabel per cell, in slice
// order. startOffset is the number of leading cells left blank on the FIRST
// page ONLY — every subsequent page starts at cell zero — so a sheet
// already partly used (some labels peeled off a previous print run) is
// filled starting from its first free cell; valid range is 0 to
// size.CellsPerPage()-1 inclusive. The labels slice is unbounded and Render
// makes no single-page assumption: pagination across as many pages as
// labels requires is Render's own job, never the caller's, and any
// print-batch cap (NSTR-50 caps a single batch at 300) is that app-layer
// caller's concern, not this port's.
//
// Error contract: ErrNoLabels when labels is empty, a wrapped
// ErrInvalidLabelSize when size fails Validate, and a wrapped
// ErrInvalidStartOffset when startOffset falls outside 0..
// size.CellsPerPage()-1.
type LabelRenderer interface {
	Render(ctx context.Context, size LabelSize, labels []BinLabel, startOffset int) (Document, error)
}
