package adapter

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"strings"

	"github.com/ericfisherdev/nestcore/qrcode"
	"github.com/signintech/gopdf"

	"github.com/ericfisherdev/nestorage/internal/labels/domain"
)

// hankenGroteskRegularTTF is the Hanken Grotesk Regular TrueType program,
// embedded straight into the binary (fonts/OFL.txt carries its license).
// Registering it per-document with AddTTFFontByReader embeds the font
// program in the OUTPUT PDF too, so a printed label never depends on the
// printer host having Hanken Grotesk installed.
//
//go:embed fonts/HankenGrotesk-Regular.ttf
var hankenGroteskRegularTTF []byte

// fontFamily is the name PDFSheetRenderer registers hankenGroteskRegularTTF
// under, and the only family every SetFont call in this file references —
// one constant so the two can never drift apart.
const fontFamily = "HankenGrotesk"

// Font sizes below are in points: gopdf's SetFont size parameter is always
// points regardless of Config.Unit — only positions and rects follow the
// configured unit (confirmed against gopdf's own createContent, which
// treats FontSize as a raw points value feeding unitsPerEm/fontSize maths).
const (
	// binNameFontSizePT is the bin name's normal size.
	binNameFontSizePT = 10.0
	// binNameReducedFontSizePT is the "one notch down" size drawText steps
	// to before resorting to truncation, per NSTR-49's plan.
	binNameReducedFontSizePT = 8.0
	// locationFontSizePT is the secondary location line's fixed size —
	// always the smaller of the two, never stepped down further.
	locationFontSizePT = 7.0
)

// cellPaddingMM is the inner padding this renderer leaves between a cell's
// own edge and anything drawn inside it — the QR block and the text region
// alike — so neither ever touches an adjacent label or the cell border.
const cellPaddingMM = 2.0

// interiorGapMM separates the QR block from the text region beside/below
// it, and the bin name line from the location line beneath it.
const interiorGapMM = 1.5

// qrModuleSize is the pixels-per-module size passed to nestcore's
// qrcode.PNGDataURI (see its own doc for the parameter's meaning).
// nestcore's standard writer adds a fixed 40px quiet-zone border regardless
// of this value (yeqown/go-qrcode/writer/standard's own default), so
// keeping this at or below 10 guarantees at least the 4-module quiet zone
// the AC requires (40/10 = 4 modules); 8 matches storage/adapter's own
// binQRModuleSize precedent and leaves 5 modules of headroom.
const qrModuleSize = 8

// pdfContentType is the Document.ContentType every successful
// PDFSheetRenderer.Render call returns.
const pdfContentType = "application/pdf"

// ellipsis is appended to a name/location line truncateToWidth must
// shorten to fit its region — a single rune, so the rune-sliced result
// never needs a second truncation pass.
const ellipsis = "…"

// PDFSheetRenderer is the domain.LabelRenderer implementation backed by
// gopdf. It carries no state of its own — hankenGroteskRegularTTF is a
// package-level constant, not a runtime dependency — so a single instance
// is safe to share and Render is safe to call concurrently.
// NewPDFSheetRenderer exists so callers depend on a named type rather than
// constructing gopdf state themselves.
type PDFSheetRenderer struct{}

// NewPDFSheetRenderer constructs a PDFSheetRenderer.
func NewPDFSheetRenderer() *PDFSheetRenderer {
	return &PDFSheetRenderer{}
}

// Render implements domain.LabelRenderer. See the port's own doc for the
// full contract; in short: ErrNoLabels for an empty labels slice, a wrapped
// ErrInvalidLabelSize when size fails Validate, a wrapped
// ErrInvalidStartOffset when startOffset falls outside
// 0..size.CellsPerPage()-1 — all checked before any document is built — and
// otherwise a Document with ContentType "application/pdf" whose Data is the
// fully rendered sheet, paginated automatically across as many pages as
// labels requires (startOffset applies to the first page only; every
// subsequent page starts at cell zero).
func (r *PDFSheetRenderer) Render(ctx context.Context, size domain.LabelSize, labels []domain.BinLabel, startOffset int) (domain.Document, error) {
	if len(labels) == 0 {
		return domain.Document{}, domain.ErrNoLabels
	}
	if err := size.Validate(); err != nil {
		return domain.Document{}, err
	}
	cellsPerPage := size.CellsPerPage()
	if startOffset < 0 || startOffset >= cellsPerPage {
		return domain.Document{}, fmt.Errorf("%w: %d", domain.ErrInvalidStartOffset, startOffset)
	}
	if err := ctx.Err(); err != nil {
		return domain.Document{}, err
	}

	pdf := &gopdf.GoPdf{}
	pdf.Start(gopdf.Config{
		Unit:     gopdf.UnitMM,
		PageSize: gopdf.Rect{W: size.PageWidthMM, H: size.PageHeightMM},
	})
	if err := pdf.AddTTFFontByReader(fontFamily, bytes.NewReader(hankenGroteskRegularTTF)); err != nil {
		return domain.Document{}, fmt.Errorf("labels: pdf sheet: load font: %w", err)
	}

	pdf.AddPage()
	cellOnPage := startOffset
	for _, label := range labels {
		if cellOnPage >= cellsPerPage {
			pdf.AddPage()
			cellOnPage = 0
		}
		if err := drawCell(pdf, size, label, cellOnPage); err != nil {
			return domain.Document{}, fmt.Errorf("labels: pdf sheet: cell %d: %w", cellOnPage, err)
		}
		cellOnPage++
	}

	return domain.Document{ContentType: pdfContentType, Data: pdf.GetBytesPdf()}, nil
}

// CellOrigin returns cell index cellOnPage's upper-left corner, in
// millimetres, within size's grid: col = cellOnPage mod size.Columns, row =
// cellOnPage div size.Columns, x = left margin + col*(cell width+column
// gutter), y = top margin + row*(cell height+row gutter) — the exact
// per-cell placement math NSTR-49's plan documents, factored out of
// Render's own loop as a pure function expressly so tests can assert the
// 30-up and 10-up geometries' expected millimetre positions directly,
// without parsing PDF content streams. cellOnPage must be in
// 0..size.CellsPerPage()-1; Render's own loop is what keeps it in range
// across pagination.
func CellOrigin(size domain.LabelSize, cellOnPage int) (xMM, yMM float64) {
	col := cellOnPage % size.Columns
	row := cellOnPage / size.Columns
	xMM = size.MarginLeftMM + float64(col)*(size.CellWidthMM+size.GutterXMM)
	yMM = size.MarginTopMM + float64(row)*(size.CellHeightMM+size.GutterYMM)
	return xMM, yMM
}

// mmRect is a millimetre-space rectangle local to this file — never
// exported, and never the gopdf.Rect type it eventually feeds, since
// gopdf.Rect carries only a width/height (position is a separate argument
// to whichever gopdf call places it); mmRect keeps a position with its size
// until the point each is actually needed.
type mmRect struct {
	X, Y, W, H float64
}

// translated returns r shifted by (dx, dy) — used to move a cell-relative
// rect (cellLayout's own return values) to the sheet-absolute position
// CellOrigin computes.
func (r mmRect) translated(dx, dy float64) mmRect {
	return mmRect{X: r.X + dx, Y: r.Y + dy, W: r.W, H: r.H}
}

// drawCell renders one BinLabel into the cell at cellOnPage: the QR code
// plus the bin name and location, laid out beside the QR for a
// landscape-shaped cell (wider than tall) or below it for a portrait one —
// the two builtin sheet geometries (Avery 5160/5163) are both landscape
// cells, single-4x6 is portrait, and this check is what lets one drawCell
// serve every registry entry with no size-specific branch elsewhere.
// BinLabel.Code is deliberately not rendered — out of this ticket's scope
// (see this package's own doc).
func drawCell(pdf *gopdf.GoPdf, size domain.LabelSize, label domain.BinLabel, cellOnPage int) error {
	cellX, cellY := CellOrigin(size, cellOnPage)
	qrRect, textRect := cellLayout(size)
	qrRect = qrRect.translated(cellX, cellY)
	textRect = textRect.translated(cellX, cellY)

	if err := drawQR(pdf, label.QRPayload, qrRect); err != nil {
		return err
	}
	return drawText(pdf, label, textRect)
}

// cellLayout splits one label cell (origin at 0,0) into the QR block and
// the text region beside or below it, per drawCell's own doc. Both come
// back inset by cellPaddingMM on every outward-facing edge, and separated
// from each other by interiorGapMM.
func cellLayout(size domain.LabelSize) (qrRect, textRect mmRect) {
	innerW := size.CellWidthMM - 2*cellPaddingMM
	innerH := size.CellHeightMM - 2*cellPaddingMM

	if size.CellWidthMM >= size.CellHeightMM {
		// Landscape: QR square on the left, sized to the cell's own
		// inner height, text region filling the remaining width to the
		// right.
		qrSize := innerH
		qrRect = mmRect{X: cellPaddingMM, Y: cellPaddingMM, W: qrSize, H: qrSize}
		textX := cellPaddingMM + qrSize + interiorGapMM
		textRect = mmRect{
			X: textX,
			Y: cellPaddingMM,
			W: size.CellWidthMM - textX - cellPaddingMM,
			H: innerH,
		}
		return qrRect, textRect
	}

	// Portrait: QR square across the top, sized to the cell's own inner
	// width, text region filling the remaining height below.
	qrSize := innerW
	qrRect = mmRect{X: cellPaddingMM, Y: cellPaddingMM, W: qrSize, H: qrSize}
	textY := cellPaddingMM + qrSize + interiorGapMM
	textRect = mmRect{
		X: cellPaddingMM,
		Y: textY,
		W: innerW,
		H: size.CellHeightMM - textY - cellPaddingMM,
	}
	return qrRect, textRect
}

// drawQR renders payload as a QR code through nestcore's qrcode package and
// places it filling qrRect exactly.
func drawQR(pdf *gopdf.GoPdf, payload string, qrRect mmRect) error {
	dataURI, err := qrcode.PNGDataURI(payload, qrModuleSize)
	if err != nil {
		return fmt.Errorf("labels: pdf sheet: render qr: %w", err)
	}
	img, err := decodeQRDataURI(dataURI)
	if err != nil {
		return err
	}
	return pdf.ImageFrom(img, qrRect.X, qrRect.Y, &gopdf.Rect{W: qrRect.W, H: qrRect.H})
}

// decodeQRDataURI decodes a "data:image/png;base64,..." URI — nestcore
// qrcode.PNGDataURI's own output shape — back into an image.Image gopdf's
// ImageFrom can place. nestcore exposes no PNGBytes function, so this small
// helper lives here, in the adapter that actually needs raw bytes, rather
// than as a change to nestcore.
func decodeQRDataURI(dataURI string) (image.Image, error) {
	const prefix = "data:image/png;base64,"
	encoded, ok := strings.CutPrefix(dataURI, prefix)
	if !ok {
		return nil, fmt.Errorf("labels: pdf sheet: qr data URI missing %q prefix", prefix)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("labels: pdf sheet: decode qr base64: %w", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("labels: pdf sheet: decode qr png: %w", err)
	}
	return img, nil
}

// drawText renders label's bin name and location into textRect: the name on
// top, fit via fitBinName (binNameFontSizePT stepped down to
// binNameReducedFontSizePT, then truncated, in that order); the location
// beneath it at the fixed, smaller locationFontSizePT, truncated the same
// way if it overflows.
func drawText(pdf *gopdf.GoPdf, label domain.BinLabel, textRect mmRect) error {
	nameRect, locationRect := splitTextRegion(textRect)

	name, nameSize, err := fitBinName(pdf, label.Name, nameRect.W)
	if err != nil {
		return fmt.Errorf("fit bin name: %w", err)
	}
	if err := drawLine(pdf, nameRect, name, nameSize); err != nil {
		return fmt.Errorf("draw bin name: %w", err)
	}

	location, err := fitLine(pdf, label.LocationName, locationFontSizePT, locationRect.W)
	if err != nil {
		return fmt.Errorf("fit location: %w", err)
	}
	if err := drawLine(pdf, locationRect, location, locationFontSizePT); err != nil {
		return fmt.Errorf("draw location: %w", err)
	}
	return nil
}

// splitTextRegion divides a text region into the bin name's line (the top
// three-fifths) and the location's line beneath it (the remainder), with
// interiorGapMM between them — the location line is always the smaller,
// secondary one, per NSTR-49's plan.
func splitTextRegion(region mmRect) (nameRect, locationRect mmRect) {
	nameH := region.H*0.6 - interiorGapMM/2
	locationY := region.Y + nameH + interiorGapMM
	nameRect = mmRect{X: region.X, Y: region.Y, W: region.W, H: nameH}
	locationRect = mmRect{X: region.X, Y: locationY, W: region.W, H: region.H - nameH - interiorGapMM}
	return nameRect, locationRect
}

// drawLine sets size (points) and draws text left/top-aligned in rect.
func drawLine(pdf *gopdf.GoPdf, rect mmRect, text string, size float64) error {
	if err := pdf.SetFont(fontFamily, "", size); err != nil {
		return err
	}
	pdf.SetXY(rect.X, rect.Y)
	return pdf.CellWithOption(&gopdf.Rect{W: rect.W, H: rect.H}, text, gopdf.CellOption{Align: gopdf.Left | gopdf.Top})
}

// fitBinName picks the largest of binNameFontSizePT/binNameReducedFontSizePT
// that fits name within maxWidthMM, truncating at the reduced size
// (fitLine's own truncateToWidth fallback) if neither does.
func fitBinName(pdf *gopdf.GoPdf, name string, maxWidthMM float64) (string, float64, error) {
	if err := pdf.SetFont(fontFamily, "", binNameFontSizePT); err != nil {
		return "", 0, err
	}
	width, err := pdf.MeasureTextWidth(name)
	if err != nil {
		return "", 0, err
	}
	if width <= maxWidthMM {
		return name, binNameFontSizePT, nil
	}

	fitted, err := fitLine(pdf, name, binNameReducedFontSizePT, maxWidthMM)
	if err != nil {
		return "", 0, err
	}
	return fitted, binNameReducedFontSizePT, nil
}

// fitLine sets size then returns text as-is if it already fits maxWidthMM,
// or truncateToWidth's output otherwise.
func fitLine(pdf *gopdf.GoPdf, text string, size, maxWidthMM float64) (string, error) {
	if err := pdf.SetFont(fontFamily, "", size); err != nil {
		return "", err
	}
	width, err := pdf.MeasureTextWidth(text)
	if err != nil {
		return "", err
	}
	if width <= maxWidthMM {
		return text, nil
	}
	return truncateToWidth(pdf, text, maxWidthMM)
}

// truncateToWidth trims text one rune at a time from the end (rune
// slicing, never byte slicing, so a multi-byte name survives intact) until
// text+ellipsis fits within maxWidthMM at the font size the caller has
// already set (fitLine always calls SetFont before this).
func truncateToWidth(pdf *gopdf.GoPdf, text string, maxWidthMM float64) (string, error) {
	runes := []rune(text)
	for len(runes) > 0 {
		candidate := string(runes) + ellipsis
		width, err := pdf.MeasureTextWidth(candidate)
		if err != nil {
			return "", err
		}
		if width <= maxWidthMM {
			return candidate, nil
		}
		runes = runes[:len(runes)-1]
	}
	return ellipsis, nil
}
