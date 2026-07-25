package adapter_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/labels/adapter"
	"github.com/ericfisherdev/nestorage/internal/labels/domain"
)

// pdfMagicBytes is every well-formed PDF's own file signature.
var pdfMagicBytes = []byte("%PDF-")

// pageObjectMarker is how a page object's own dictionary is written to the
// (uncompressed) object list — gopdf flate-compresses content streams but
// not the object dictionaries themselves, so counting this substring in the
// raw output is a reliable page count without ever parsing a content
// stream. The trailing newline is what keeps this from also matching the
// document's own "/Type /Pages" root object.
var pageObjectMarker = []byte("/Type /Page\n")

var _ domain.LabelRenderer = (*adapter.PDFSheetRenderer)(nil)

// countingContext is a context.Context whose Err() returns nil for the
// first passLimit calls and context.Canceled on every call after that.
// Render calls ctx.Err() once before the loop starts and again on every
// loop iteration (see pdf_sheet.go's own comment on why): a passLimit high
// enough to clear the pre-loop check but not the first couple of
// iterations is what proves the in-loop recheck actually runs, rather than
// only the one before rendering begins.
type countingContext struct {
	context.Context
	calls     int
	passLimit int
}

func (c *countingContext) Err() error {
	c.calls++
	if c.calls > c.passLimit {
		return context.Canceled
	}
	return nil
}

func testLabels(n int) []domain.BinLabel {
	labels := make([]domain.BinLabel, n)
	for i := range labels {
		labels[i] = domain.BinLabel{
			Code:         "A1",
			Name:         "Bin",
			LocationName: "Garage",
			QRPayload:    "https://example.test/b/A1",
		}
	}
	return labels
}

// smallLabelSize is a deliberately tiny geometry (2 cells per page) so
// pagination tests run fast and the expected page count is easy to reason
// about by hand.
func smallLabelSize() domain.LabelSize {
	return domain.LabelSize{
		ID:          "test-2up",
		DisplayName: "Test 2-up",
		PageWidthMM: 100, PageHeightMM: 60,
		MarginTopMM: 5, MarginBottomMM: 5, MarginLeftMM: 5, MarginRightMM: 5,
		Columns: 2, Rows: 1,
		CellWidthMM: 44, CellHeightMM: 50,
		GutterXMM: 2,
	}
}

func TestPDFSheetRenderer_Render_EmptyLabelsRejected(t *testing.T) {
	renderer := adapter.NewPDFSheetRenderer()
	_, err := renderer.Render(context.Background(), smallLabelSize(), nil, 0)
	if !errors.Is(err, domain.ErrNoLabels) {
		t.Errorf("Render(no labels) error = %v, want ErrNoLabels", err)
	}
}

func TestPDFSheetRenderer_Render_InvalidSizeRejected(t *testing.T) {
	renderer := adapter.NewPDFSheetRenderer()
	invalid := domain.LabelSize{} // blank id, zero dimensions — fails Validate
	_, err := renderer.Render(context.Background(), invalid, testLabels(1), 0)
	if !errors.Is(err, domain.ErrInvalidLabelSize) {
		t.Errorf("Render(invalid size) error = %v, want ErrInvalidLabelSize", err)
	}
}

// TestPDFSheetRenderer_Render_ContextCancelledMidLoop covers the AC a
// human reviewer raised: Render must stop drawing once its context is
// cancelled or times out, even mid-batch, rather than only checking once
// before the first label. passLimit: 2 clears the pre-loop check (call 1)
// and the first iteration's check (call 2), then cancels on the second
// iteration's check (call 3) — with 5 labels queued, only the first can
// possibly get drawn.
func TestPDFSheetRenderer_Render_ContextCancelledMidLoop(t *testing.T) {
	renderer := adapter.NewPDFSheetRenderer()
	ctx := &countingContext{Context: context.Background(), passLimit: 2}

	_, err := renderer.Render(ctx, smallLabelSize(), testLabels(5), 0)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Render() error = %v, want context.Canceled", err)
	}
	if ctx.calls < 3 {
		t.Errorf("ctx.Err() was called %d times, want at least 3 (proves the loop rechecks, not just the pre-loop check)", ctx.calls)
	}
}

func TestPDFSheetRenderer_Render_StartOffsetOutOfRangeRejected(t *testing.T) {
	renderer := adapter.NewPDFSheetRenderer()
	size := smallLabelSize()

	tests := []struct {
		name        string
		startOffset int
	}{
		{"negative", -1},
		{"equal to CellsPerPage", size.CellsPerPage()},
		{"beyond CellsPerPage", size.CellsPerPage() + 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := renderer.Render(context.Background(), size, testLabels(1), tt.startOffset)
			if !errors.Is(err, domain.ErrInvalidStartOffset) {
				t.Errorf("Render(startOffset=%d) error = %v, want ErrInvalidStartOffset", tt.startOffset, err)
			}
		})
	}
}

func TestPDFSheetRenderer_Render_ProducesValidPDF(t *testing.T) {
	renderer := adapter.NewPDFSheetRenderer()
	doc, err := renderer.Render(context.Background(), smallLabelSize(), testLabels(1), 0)
	if err != nil {
		t.Fatalf("Render() error = %v, want nil", err)
	}
	if doc.ContentType != "application/pdf" {
		t.Errorf("Render() ContentType = %q, want %q", doc.ContentType, "application/pdf")
	}
	if len(doc.Data) == 0 {
		t.Fatal("Render() Data is empty, want a rendered PDF")
	}
	if !bytes.HasPrefix(doc.Data, pdfMagicBytes) {
		t.Errorf("Render() Data does not start with %q", pdfMagicBytes)
	}
}

func TestPDFSheetRenderer_Render_PaginatesAcrossSheets(t *testing.T) {
	size := smallLabelSize() // 2 cells per page
	renderer := adapter.NewPDFSheetRenderer()

	tests := []struct {
		name        string
		labelCount  int
		startOffset int
		wantPages   int
	}{
		{"exactly one sheet", 2, 0, 1},
		{"one more label than a sheet holds", 3, 0, 2},
		{"two full sheets plus one", 5, 0, 3},
		{"start offset uses up first sheet's last cell", 3, 1, 2},
		// Discriminates an honored startOffset from an ignored one: with 2
		// labels and cellsPerPage=2, a zero offset fits both on one sheet
		// (1 page), but starting at offset 1 leaves only the second cell
		// free, so the second label spills onto a new sheet (2 pages). A
		// Render that silently dropped startOffset would report 1 page
		// here, not 2 — unlike the "uses up first sheet's last cell" case
		// above, whose expected page count happens to match either way.
		{"start offset actually shifts drawing, not just page count", 2, 1, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := renderer.Render(context.Background(), size, testLabels(tt.labelCount), tt.startOffset)
			if err != nil {
				t.Fatalf("Render() error = %v, want nil", err)
			}
			gotPages := bytes.Count(doc.Data, pageObjectMarker)
			if gotPages != tt.wantPages {
				t.Errorf("Render(%d labels, offset %d) page count = %d, want %d", tt.labelCount, tt.startOffset, gotPages, tt.wantPages)
			}
		})
	}
}

// TestCellOrigin_KnownGeometries asserts CellOrigin's millimetre math
// directly against the registry's own 30-up (Avery 5160) and 10-up (Avery
// 5163) geometries, without ever parsing a PDF content stream.
func TestCellOrigin_KnownGeometries(t *testing.T) {
	registry, err := domain.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() error = %v, want nil", err)
	}
	avery5160, err := registry.Get(domain.LabelSizeAvery5160)
	if err != nil {
		t.Fatalf("Get(avery5160) error = %v, want nil", err)
	}
	avery5163, err := registry.Get(domain.LabelSizeAvery5163)
	if err != nil {
		t.Fatalf("Get(avery5163) error = %v, want nil", err)
	}

	tests := []struct {
		name         string
		size         domain.LabelSize
		cellOnPage   int
		wantX, wantY float64
	}{
		{
			name: "5160 first cell", size: avery5160, cellOnPage: 0,
			wantX: avery5160.MarginLeftMM, wantY: avery5160.MarginTopMM,
		},
		{
			name: "5160 second column, first row", size: avery5160, cellOnPage: 1,
			wantX: avery5160.MarginLeftMM + avery5160.CellWidthMM + avery5160.GutterXMM,
			wantY: avery5160.MarginTopMM,
		},
		{
			name: "5160 first column, second row", size: avery5160, cellOnPage: 3,
			wantX: avery5160.MarginLeftMM,
			wantY: avery5160.MarginTopMM + avery5160.CellHeightMM + avery5160.GutterYMM,
		},
		{
			name: "5163 first cell", size: avery5163, cellOnPage: 0,
			wantX: avery5163.MarginLeftMM, wantY: avery5163.MarginTopMM,
		},
		{
			name: "5163 first column, second row", size: avery5163, cellOnPage: 2,
			wantX: avery5163.MarginLeftMM,
			wantY: avery5163.MarginTopMM + avery5163.CellHeightMM + avery5163.GutterYMM,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotX, gotY := adapter.CellOrigin(tt.size, tt.cellOnPage)
			if gotX != tt.wantX || gotY != tt.wantY {
				t.Errorf("CellOrigin(cell %d) = (%v, %v), want (%v, %v)", tt.cellOnPage, gotX, gotY, tt.wantX, tt.wantY)
			}
		})
	}
}

// TestCellOrigin_DifferentIndicesYieldDifferentPositions covers the
// millimetre-math fact Render's startOffset handling actually rests on:
// CellOrigin(size, 0) and CellOrigin(size, N) resolve to different
// positions for N != 0. This is deliberately just CellOrigin's own pure
// math, not an end-to-end proof that Render honors startOffset — Render
// simply seeds its loop with cellOnPage := startOffset (see pdf_sheet.go),
// so that wiring itself is what
// TestPDFSheetRenderer_Render_PaginatesAcrossSheets's "start offset
// actually shifts drawing" case proves, via an observable page-count
// difference a dropped startOffset would fail.
func TestCellOrigin_DifferentIndicesYieldDifferentPositions(t *testing.T) {
	registry, err := domain.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() error = %v, want nil", err)
	}
	size, err := registry.Get(domain.LabelSizeAvery5163)
	if err != nil {
		t.Fatalf("Get(avery5163) error = %v, want nil", err)
	}

	x0, y0 := adapter.CellOrigin(size, 0)
	x1, y1 := adapter.CellOrigin(size, 3)
	if x0 == x1 && y0 == y1 {
		t.Error("CellOrigin(0) and CellOrigin(3) match — a nonzero start offset must shift the first drawn cell")
	}
}
