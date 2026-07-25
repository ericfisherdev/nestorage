package adapter

// This file is package adapter (white-box), not adapter_test — the same
// narrow exception web_common_internal_test.go and registry_internal_test.go
// document (see their own docs), needed here because hankenGroteskRegularTTF,
// fitBinName, fitLine, truncateToWidth, cellLayout, splitTextRegion,
// decodeQRDataURI, and drawCell are all unexported: a black-box test cannot
// construct a *gopdf.GoPdf with the embedded font already loaded, which
// MeasureTextWidth (and therefore every text-fitting helper) requires.

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ericfisherdev/nestcore/qrcode"
	"github.com/signintech/gopdf"

	"github.com/ericfisherdev/nestorage/internal/labels/domain"
)

// newTestPDF returns a *gopdf.GoPdf already Start-ed in millimetres with
// hankenGroteskRegularTTF loaded under fontFamily — every helper this file
// tests directly needs exactly this much state before it can call
// MeasureTextWidth or draw anything.
func newTestPDF(t *testing.T) *gopdf.GoPdf {
	t.Helper()
	pdf := &gopdf.GoPdf{}
	pdf.Start(gopdf.Config{Unit: gopdf.UnitMM, PageSize: gopdf.Rect{W: 200, H: 200}})
	if err := pdf.AddTTFFontByReader(fontFamily, bytes.NewReader(hankenGroteskRegularTTF)); err != nil {
		t.Fatalf("AddTTFFontByReader() error = %v, want nil", err)
	}
	return pdf
}

// measureWidthAtSize sets pdf's font to size (points) and measures text's
// width, failing the test immediately on either call's error — the shared
// "set then measure" pattern every width-dependent test in this file needs
// before it can assert anything about a rendered width.
func measureWidthAtSize(t *testing.T, pdf *gopdf.GoPdf, text string, size float64) float64 {
	t.Helper()
	if err := pdf.SetFont(fontFamily, "", size); err != nil {
		t.Fatalf("SetFont(%v) error = %v, want nil", size, err)
	}
	width, err := pdf.MeasureTextWidth(text)
	if err != nil {
		t.Fatalf("MeasureTextWidth() error = %v, want nil", err)
	}
	return width
}

// TestFitBinName_StepsDownBeforeTruncating covers the AC-relevant behavior:
// a name that only overflows at the normal size is drawn unchanged at the
// reduced size (no truncation), and only a name that overflows even at the
// reduced size gets truncated — the "one notch down, then truncate" order
// NSTR-49's plan specifies. Each case's own assertions live in a named
// helper (assertFitsWithoutTruncating/assertTruncatesToFit) rather than
// inline in the t.Run closures, keeping this test itself a short setup-and-
// dispatch function.
func TestFitBinName_StepsDownBeforeTruncating(t *testing.T) {
	pdf := newTestPDF(t)
	const name = "Extra Long Pantry Overflow Shelf Contents"

	normalWidth := measureWidthAtSize(t, pdf, name, binNameFontSizePT)
	reducedWidth := measureWidthAtSize(t, pdf, name, binNameReducedFontSizePT)
	if reducedWidth >= normalWidth {
		t.Fatalf("reducedWidth = %v, want < normalWidth (%v) — test fixture assumption broken", reducedWidth, normalWidth)
	}

	t.Run("fits at reduced size without truncating", func(t *testing.T) {
		// Between the two widths: overflows normal, fits reduced.
		assertFitsWithoutTruncating(t, pdf, name, (normalWidth+reducedWidth)/2)
	})
	t.Run("truncates when even reduced size overflows", func(t *testing.T) {
		assertTruncatesToFit(t, pdf, name, reducedWidth/2)
	})
}

// assertFitsWithoutTruncating asserts fitBinName steps down to
// binNameReducedFontSizePT and returns name unchanged, for a maxWidth the
// reduced size alone is enough to fit.
func assertFitsWithoutTruncating(t *testing.T, pdf *gopdf.GoPdf, name string, maxWidth float64) {
	t.Helper()
	got, size, err := fitBinName(pdf, name, maxWidth)
	if err != nil {
		t.Fatalf("fitBinName() error = %v, want nil", err)
	}
	if size != binNameReducedFontSizePT {
		t.Errorf("fitBinName() size = %v, want %v", size, binNameReducedFontSizePT)
	}
	if got != name {
		t.Errorf("fitBinName() = %q, want unchanged %q", got, name)
	}
}

// assertTruncatesToFit asserts fitBinName truncates name to an
// ellipsis-terminated result that never measures wider than maxWidth, for a
// maxWidth even the reduced size overflows.
func assertTruncatesToFit(t *testing.T, pdf *gopdf.GoPdf, name string, maxWidth float64) {
	t.Helper()
	got, size, err := fitBinName(pdf, name, maxWidth)
	if err != nil {
		t.Fatalf("fitBinName() error = %v, want nil", err)
	}
	if size != binNameReducedFontSizePT {
		t.Errorf("fitBinName() size = %v, want %v", size, binNameReducedFontSizePT)
	}
	if !strings.HasSuffix(got, ellipsis) {
		t.Errorf("fitBinName() = %q, want a truncated name ending in %q", got, ellipsis)
	}
	gotWidth := measureWidthAtSize(t, pdf, got, size)
	if gotWidth > maxWidth {
		t.Errorf("fitBinName() width = %v, want <= %v (never wider than its region)", gotWidth, maxWidth)
	}
}

// TestTruncateToWidth_NeverWiderThanRegion covers the AC directly: across a
// range of shrinking widths, truncateToWidth's own output never measures
// wider than the width it was given.
func TestTruncateToWidth_NeverWiderThanRegion(t *testing.T) {
	pdf := newTestPDF(t)
	const name = "Basement Overflow Storage Bin For Seasonal Decorations"
	fullWidth := measureWidthAtSize(t, pdf, name, binNameReducedFontSizePT)

	for _, fraction := range []float64{0.75, 0.5, 0.25, 0.1} {
		maxWidth := fullWidth * fraction
		t.Run("", func(t *testing.T) {
			assertTruncateFitsWidth(t, pdf, name, maxWidth)
		})
	}
}

// assertTruncateFitsWidth asserts truncateToWidth's own output never
// measures wider than maxWidth.
func assertTruncateFitsWidth(t *testing.T, pdf *gopdf.GoPdf, name string, maxWidth float64) {
	t.Helper()
	got, err := truncateToWidth(pdf, name, maxWidth)
	if err != nil {
		t.Fatalf("truncateToWidth() error = %v, want nil", err)
	}
	gotWidth := measureWidthAtSize(t, pdf, got, binNameReducedFontSizePT)
	if gotWidth > maxWidth {
		t.Errorf("truncateToWidth(%v) width = %v, want <= %v", maxWidth, gotWidth, maxWidth)
	}
}

// TestTruncateToWidth_MultiByteNamesSurviveIntact covers the AC's own
// wording directly: rune slicing, never byte slicing, so a multi-byte bin
// name is never cut mid-character.
func TestTruncateToWidth_MultiByteNamesSurviveIntact(t *testing.T) {
	pdf := newTestPDF(t)
	const name = "Café Storage Bin — Décor Basket 日本語ラベル"
	fullWidth := measureWidthAtSize(t, pdf, name, binNameReducedFontSizePT)

	got, err := truncateToWidth(pdf, name, fullWidth*0.4)
	if err != nil {
		t.Fatalf("truncateToWidth() error = %v, want nil", err)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncateToWidth() = %q, want valid UTF-8", got)
	}
	if !strings.HasSuffix(got, ellipsis) {
		t.Fatalf("truncateToWidth() = %q, want it to end in %q", got, ellipsis)
	}

	kept := []rune(strings.TrimSuffix(got, ellipsis))
	original := []rune(name)
	if len(kept) > len(original) {
		t.Fatalf("truncateToWidth() kept %d runes, more than the original %d", len(kept), len(original))
	}
	for i, r := range kept {
		if r != original[i] {
			t.Fatalf("truncateToWidth() rune %d = %q, want %q (original prefix must survive unmodified)", i, r, original[i])
		}
	}
}

// TestCellLayout_Landscape covers a wider-than-tall cell (Avery 5160/5163's
// own shape): the QR block is a square sized to the cell's inner height,
// placed at the top-left padding inset, with the text region filling the
// remaining width to its right.
func TestCellLayout_Landscape(t *testing.T) {
	size := domain.LabelSize{CellWidthMM: 66.675, CellHeightMM: 25.4}

	qr, text := cellLayout(size)

	wantQRSize := size.CellHeightMM - 2*cellPaddingMM
	if qr.X != cellPaddingMM || qr.Y != cellPaddingMM {
		t.Errorf("cellLayout() qr origin = (%v, %v), want (%v, %v)", qr.X, qr.Y, cellPaddingMM, cellPaddingMM)
	}
	if qr.W != wantQRSize || qr.H != wantQRSize {
		t.Errorf("cellLayout() qr size = (%v, %v), want (%v, %v)", qr.W, qr.H, wantQRSize, wantQRSize)
	}

	wantTextX := cellPaddingMM + wantQRSize + interiorGapMM
	if text.X != wantTextX || text.Y != cellPaddingMM {
		t.Errorf("cellLayout() text origin = (%v, %v), want (%v, %v)", text.X, text.Y, wantTextX, cellPaddingMM)
	}
	wantTextW := size.CellWidthMM - wantTextX - cellPaddingMM
	wantTextH := size.CellHeightMM - 2*cellPaddingMM
	if text.W != wantTextW || text.H != wantTextH {
		t.Errorf("cellLayout() text size = (%v, %v), want (%v, %v)", text.W, text.H, wantTextW, wantTextH)
	}
}

// TestCellLayout_Portrait covers a taller-than-wide cell (single-4x6's own
// shape): the QR block is a square sized to the cell's inner width, placed
// at the top-left padding inset, with the text region filling the
// remaining height beneath it.
func TestCellLayout_Portrait(t *testing.T) {
	size := domain.LabelSize{CellWidthMM: 101.6, CellHeightMM: 152.4}

	qr, text := cellLayout(size)

	wantQRSize := size.CellWidthMM - 2*cellPaddingMM
	if qr.X != cellPaddingMM || qr.Y != cellPaddingMM {
		t.Errorf("cellLayout() qr origin = (%v, %v), want (%v, %v)", qr.X, qr.Y, cellPaddingMM, cellPaddingMM)
	}
	if qr.W != wantQRSize || qr.H != wantQRSize {
		t.Errorf("cellLayout() qr size = (%v, %v), want (%v, %v)", qr.W, qr.H, wantQRSize, wantQRSize)
	}

	wantTextY := cellPaddingMM + wantQRSize + interiorGapMM
	if text.X != cellPaddingMM || text.Y != wantTextY {
		t.Errorf("cellLayout() text origin = (%v, %v), want (%v, %v)", text.X, text.Y, cellPaddingMM, wantTextY)
	}
	wantTextW := size.CellWidthMM - 2*cellPaddingMM
	wantTextH := size.CellHeightMM - wantTextY - cellPaddingMM
	if text.W != wantTextW || text.H != wantTextH {
		t.Errorf("cellLayout() text size = (%v, %v), want (%v, %v)", text.W, text.H, wantTextW, wantTextH)
	}
}

// TestSplitTextRegion covers the name/location vertical split: the name
// gets the top three-fifths (less half the interior gap), the location
// gets the remainder, and interiorGapMM separates them.
func TestSplitTextRegion(t *testing.T) {
	region := mmRect{X: 5, Y: 5, W: 40, H: 20}

	name, location := splitTextRegion(region)

	wantNameH := region.H*0.6 - interiorGapMM/2
	if name.X != region.X || name.Y != region.Y || name.W != region.W || name.H != wantNameH {
		t.Errorf("splitTextRegion() name = %+v, want X=%v Y=%v W=%v H=%v", name, region.X, region.Y, region.W, wantNameH)
	}

	wantLocationY := region.Y + wantNameH + interiorGapMM
	wantLocationH := region.H - wantNameH - interiorGapMM
	if location.X != region.X || location.Y != wantLocationY || location.W != region.W || location.H != wantLocationH {
		t.Errorf("splitTextRegion() location = %+v, want X=%v Y=%v W=%v H=%v", location, region.X, wantLocationY, region.W, wantLocationH)
	}
}

// TestMMRect_Translated covers the plain coordinate shift CellOrigin's
// sheet-absolute position and cellLayout's cell-relative rects are combined
// through.
func TestMMRect_Translated(t *testing.T) {
	r := mmRect{X: 1, Y: 2, W: 3, H: 4}
	got := r.translated(10, 20)
	want := mmRect{X: 11, Y: 22, W: 3, H: 4}
	if got != want {
		t.Errorf("translated() = %+v, want %+v", got, want)
	}
}

// TestDecodeQRDataURI_RoundTrip covers the AC directly: nestcore
// qrcode.PNGDataURI's own output decodes back into a valid image via this
// adapter's own helper — no PNGBytes function needed from nestcore.
func TestDecodeQRDataURI_RoundTrip(t *testing.T) {
	dataURI, err := qrcode.PNGDataURI("https://example.test/b/A1", qrModuleSize)
	if err != nil {
		t.Fatalf("qrcode.PNGDataURI() error = %v, want nil", err)
	}

	img, err := decodeQRDataURI(dataURI)
	if err != nil {
		t.Fatalf("decodeQRDataURI() error = %v, want nil", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		t.Errorf("decodeQRDataURI() image bounds = %v, want positive width and height", bounds)
	}
}

// TestDecodeQRDataURI_Errors covers each rejection path: a missing data URI
// prefix, invalid base64, and base64-valid-but-not-PNG bytes.
func TestDecodeQRDataURI_Errors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"missing data URI prefix", "not-a-data-uri"},
		{"invalid base64", "data:image/png;base64,not-valid-base64!!!"},
		{"base64-valid but not a PNG", "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("not a png"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeQRDataURI(tt.input); err == nil {
				t.Error("decodeQRDataURI() error = nil, want non-nil")
			}
		})
	}
}

// TestDrawCell_Smoke drives drawCell end to end against a real *gopdf.GoPdf
// page — the same call Render's own loop makes — covering drawQR, drawText,
// and everything they call in one integration pass, since none of those
// unexported functions are reachable from a black-box test.
func TestDrawCell_Smoke(t *testing.T) {
	size := domain.LabelSize{
		PageWidthMM: 100, PageHeightMM: 100,
		MarginTopMM: 5, MarginBottomMM: 5, MarginLeftMM: 5, MarginRightMM: 5,
		Columns: 1, Rows: 1,
		CellWidthMM: 90, CellHeightMM: 90,
	}
	label := domain.BinLabel{
		Code:         "A1",
		Name:         "Extra Long Pantry Overflow Shelf Contents Bin",
		LocationName: "Garage — Shelf 2",
		QRPayload:    "https://example.test/b/A1",
	}

	pdf := newTestPDF(t)
	pdf.AddPage()
	if err := drawCell(pdf, size, label, 0); err != nil {
		t.Fatalf("drawCell() error = %v, want nil", err)
	}
}
