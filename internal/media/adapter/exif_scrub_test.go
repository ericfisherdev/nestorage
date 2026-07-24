package adapter_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	goexif "github.com/xor-gate/goexif2/exif"

	"github.com/ericfisherdev/nestorage/internal/media/adapter"
	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

// --- hand-built TIFF/EXIF fixtures ------------------------------------------
//
// The standard library has no EXIF ENCODER (only goexif2, decode-only), so a
// real GPS+Orientation fixture has to be hand-assembled at the byte level —
// exactly the TIFF 6.0 structure a camera would write, verified against
// goexif2's own decoder source (exif.go/tiff/tag.go) rather than guessed.

const (
	tiffTagOrientation = 0x0112
	tiffTagGPSPointer  = 0x8825
	tiffTypeASCII      = 2
	tiffTypeShort      = 3
	tiffTypeLong       = 4
	tiffTypeRational   = 5

	gpsTagLatRef = 0x0001
	gpsTagLat    = 0x0002
	gpsTagLonRef = 0x0003
	gpsTagLon    = 0x0004
)

// writeIFDEntry appends one 12-byte little-endian TIFF IFD entry (TIFF 6.0
// sec 2): 2-byte tag, 2-byte type, 4-byte count, then a 4-byte
// value-or-offset field left-justified per the spec (value's own bytes go in
// value[:], the rest is caller-supplied zero padding).
func writeIFDEntry(buf *bytes.Buffer, tag, typ uint16, count uint32, value [4]byte) {
	var entry [12]byte
	binary.LittleEndian.PutUint16(entry[0:2], tag)
	binary.LittleEndian.PutUint16(entry[2:4], typ)
	binary.LittleEndian.PutUint32(entry[4:8], count)
	copy(entry[8:12], value[:])
	buf.Write(entry[:])
}

func le32(v uint32) [4]byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return b
}

// asciiInline packs s (plus its NUL terminator) into a TIFF entry's inline
// 4-byte value field — valid only for an ASCII tag whose count (len(s)+1)
// is <= 4, which every GPS ref tag ("N\0", "W\0", ...) is.
func asciiInline(s string) [4]byte {
	var b [4]byte
	copy(b[:], s+"\x00")
	return b
}

func writeRational(buf *bytes.Buffer, num, den uint32) {
	var b [8]byte
	binary.LittleEndian.PutUint32(b[0:4], num)
	binary.LittleEndian.PutUint32(b[4:8], den)
	buf.Write(b[:])
}

// buildEXIFBlob returns a minimal little-endian TIFF/EXIF blob — the bytes
// that follow the "Exif\0\0" signature in a JPEG APP1 segment: IFD0 carrying
// an Orientation tag (skipped when orientation <= 0, modeling "no
// orientation tag at all") and, when withGPS, a GPSInfoIFDPointer plus a
// hand-built GPS sub-IFD with a fixed non-zero latitude/longitude (40°26'
// 46.302"N, 79°58'55.998"W — Pittsburgh's coordinates, chosen only for being
// a memorable, plausible "home" fixture). Every offset below is computed
// analytically from the known, fixed shape of this exact IFD layout, not
// discovered by writing then patching.
func buildEXIFBlob(t *testing.T, orientation int, withGPS bool) []byte {
	t.Helper()

	ifd0EntryCount := 0
	if orientation > 0 {
		ifd0EntryCount++
	}
	if withGPS {
		ifd0EntryCount++
	}
	const ifd0Start = 8
	ifd0Size := 2 + 12*ifd0EntryCount + 4
	gpsIFDStart := ifd0Start + ifd0Size
	const gpsEntryCount = 4
	gpsIFDSize := 2 + 12*gpsEntryCount + 4
	gpsDataStart := gpsIFDStart + gpsIFDSize
	latDataOffset := uint32(gpsDataStart)
	lonDataOffset := uint32(gpsDataStart + 24)

	var blob bytes.Buffer
	blob.WriteString("II")
	blob.Write([]byte{0x2A, 0x00})
	_ = binary.Write(&blob, binary.LittleEndian, uint32(ifd0Start))

	var ifd0 bytes.Buffer
	_ = binary.Write(&ifd0, binary.LittleEndian, uint16(ifd0EntryCount))
	if orientation > 0 {
		writeIFDEntry(&ifd0, tiffTagOrientation, tiffTypeShort, 1, le32(uint32(orientation)))
	}
	if withGPS {
		writeIFDEntry(&ifd0, tiffTagGPSPointer, tiffTypeLong, 1, le32(uint32(gpsIFDStart)))
	}
	_ = binary.Write(&ifd0, binary.LittleEndian, uint32(0)) // no next IFD
	if ifd0.Len() != ifd0Size {
		t.Fatalf("buildEXIFBlob: ifd0 size = %d, want %d (offset math would be wrong)", ifd0.Len(), ifd0Size)
	}
	blob.Write(ifd0.Bytes())

	if withGPS {
		var gpsIFD bytes.Buffer
		_ = binary.Write(&gpsIFD, binary.LittleEndian, uint16(gpsEntryCount))
		writeIFDEntry(&gpsIFD, gpsTagLatRef, tiffTypeASCII, 2, asciiInline("N"))
		writeIFDEntry(&gpsIFD, gpsTagLat, tiffTypeRational, 3, le32(latDataOffset))
		writeIFDEntry(&gpsIFD, gpsTagLonRef, tiffTypeASCII, 2, asciiInline("W"))
		writeIFDEntry(&gpsIFD, gpsTagLon, tiffTypeRational, 3, le32(lonDataOffset))
		_ = binary.Write(&gpsIFD, binary.LittleEndian, uint32(0))
		if gpsIFD.Len() != gpsIFDSize {
			t.Fatalf("buildEXIFBlob: gps IFD size = %d, want %d", gpsIFD.Len(), gpsIFDSize)
		}
		blob.Write(gpsIFD.Bytes())

		writeRational(&blob, 40, 1)       // 40 deg
		writeRational(&blob, 26, 1)       // 26 min
		writeRational(&blob, 46302, 1000) // 46.302 sec
		writeRational(&blob, 79, 1)
		writeRational(&blob, 58, 1)
		writeRational(&blob, 55998, 1000)
	}

	return blob.Bytes()
}

// jpegAPP1Segment wraps payload (expected to start with "Exif\0\0" for an
// EXIF segment) in a JPEG APP1 marker segment: FFE1 + big-endian 2-byte
// length (including the length field itself) + payload.
func jpegAPP1Segment(payload []byte) []byte {
	var lenBytes [2]byte
	binary.BigEndian.PutUint16(lenBytes[:], uint16(len(payload)+2))
	seg := make([]byte, 0, 4+len(payload))
	seg = append(seg, 0xFF, 0xE1)
	seg = append(seg, lenBytes[:]...)
	return append(seg, payload...)
}

// jpegWithEXIF inserts an APP1 EXIF segment carrying tiffBlob right after
// base's SOI marker, ahead of every other segment (JFIF APP0, DQT, SOF0,
// ...) base's own encoder wrote — the same position a real camera places it.
func jpegWithEXIF(t *testing.T, base []byte, tiffBlob []byte) []byte {
	t.Helper()
	if len(base) < 2 || base[0] != 0xFF || base[1] != 0xD8 {
		t.Fatalf("jpegWithEXIF: base does not start with SOI")
	}
	exifPayload := append([]byte("Exif\x00\x00"), tiffBlob...)
	out := make([]byte, 0, len(base)+len(exifPayload)+4)
	out = append(out, 0xFF, 0xD8)
	out = append(out, jpegAPP1Segment(exifPayload)...)
	out = append(out, base[2:]...)
	return out
}

// --- test images -------------------------------------------------------

// quadrantJPEGBytes encodes a w-by-h JPEG (w,h both even and >=16, multiples
// of 8 so quadrant boundaries land on JPEG block boundaries) with four
// distinct solid-color quadrants — TL red, TR green, BL blue, BR yellow —
// so a caller can verify a rotation baked the pixels upright by checking
// which quadrant each color ends up in, not just that dimensions swapped.
func quadrantJPEGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	halfW, halfH := w/2, h/2
	tl := color.RGBA{R: 220, G: 20, B: 20, A: 255}
	tr := color.RGBA{R: 20, G: 200, B: 20, A: 255}
	bl := color.RGBA{R: 20, G: 20, B: 220, A: 255}
	br := color.RGBA{R: 220, G: 200, B: 20, A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			switch {
			case x < halfW && y < halfH:
				img.Set(x, y, tl)
			case x >= halfW && y < halfH:
				img.Set(x, y, tr)
			case x < halfW && y >= halfH:
				img.Set(x, y, bl)
			default:
				img.Set(x, y, br)
			}
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	return buf.Bytes()
}

// approxColorEqual reports whether got and want match within tolerance on
// each of R/G/B (image/color's own 16-bit-per-channel scale), loose enough
// to absorb JPEG re-encode quantization noise on an otherwise-solid color
// block while still failing on a wrong-quadrant color (whose channels differ
// by tens of thousands, not a lossy-compression jitter of a few hundred).
func approxColorEqual(got color.Color, want color.RGBA, tolerance uint32) bool {
	gr, gg, gb, _ := got.RGBA()
	wr, wg, wb, _ := want.RGBA()
	diff := func(a, b uint32) uint32 {
		if a > b {
			return a - b
		}
		return b - a
	}
	return diff(gr, wr) <= tolerance && diff(gg, wg) <= tolerance && diff(gb, wb) <= tolerance
}

const colorTolerance = 12000

// --- tests -----------------------------------------------------------------

// gpsOrientedJPEGFixture builds the ticket-mandated fixture: a real JPEG
// carrying both GPS coordinates and EXIF Orientation 6, with quadrant-colored
// pixels so both the GPS-removal and the orientation-upright assertions run
// against the SAME fixture.
func gpsOrientedJPEGFixture(t *testing.T) []byte {
	t.Helper()
	base := quadrantJPEGBytes(t, 16, 8)
	tiff := buildEXIFBlob(t, 6, true)
	fixture := jpegWithEXIF(t, base, tiff)

	// Sanity-check the fixture itself carries what the test needs before
	// using it to test scrubbing — otherwise a broken fixture would make
	// every assertion below vacuously true.
	x, err := goexif.Decode(bytes.NewReader(fixture))
	if err != nil {
		t.Fatalf("fixture sanity check: goexif2 could not decode EXIF: %v", err)
	}
	if _, _, err := x.LatLong(); err != nil {
		t.Fatalf("fixture sanity check: LatLong() = %v, want a real GPS fix", err)
	}
	if tag, err := x.Get(goexif.Orientation); err != nil {
		t.Fatalf("fixture sanity check: no Orientation tag: %v", err)
	} else if v, _ := tag.Int(0); v != 6 {
		t.Fatalf("fixture sanity check: Orientation = %d, want 6", v)
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(fixture)); err != nil || cfg.Width != 16 || cfg.Height != 8 {
		t.Fatalf("fixture sanity check: dimensions = %+v, err=%v, want 16x8", cfg, err)
	}
	return fixture
}

// TestScrubRemovesGPSFromStoredBytes is the ticket's core privacy assertion:
// a household photo's GPS fix must not survive scrubbing, checked two ways —
// goexif2 finds no GPS data at all, and a raw byte scan finds no EXIF APP1
// signature anywhere in the output.
func TestScrubRemovesGPSFromStoredBytes(t *testing.T) {
	fixture := gpsOrientedJPEGFixture(t)
	scrubber := adapter.NewExifScrubber()

	got, err := scrubber.Scrub(context.Background(), fixture, domain.ContentTypeJPEG)
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}

	if x, err := goexif.Decode(bytes.NewReader(got)); err == nil {
		if _, _, latLongErr := x.LatLong(); latLongErr == nil {
			t.Fatal("Scrub output still carries a readable GPS fix")
		} else if !goexif.IsTagNotPresentError(latLongErr) {
			t.Fatalf("LatLong() on scrubbed output = %v, want a tag-not-present error", latLongErr)
		}
	}

	if bytes.Contains(got, []byte("Exif\x00\x00")) {
		t.Fatal("Scrub output still contains an EXIF APP1 signature byte-for-byte")
	}
}

// TestScrubBakesOrientationUpright covers the ticket's other core
// assertion — stripping EXIF must never silently rotate the photo. The
// Orientation-6 fixture is 16x8 (landscape); scrubbed, it must decode as
// 8x16 (portrait) with each quadrant's color rotated 90° clockwise into its
// upright position, and the Orientation tag itself must be gone.
func TestScrubBakesOrientationUpright(t *testing.T) {
	fixture := gpsOrientedJPEGFixture(t)
	scrubber := adapter.NewExifScrubber()

	got, err := scrubber.Scrub(context.Background(), fixture, domain.ContentTypeJPEG)
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}

	img, _, err := image.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("decode scrubbed output: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 8 || b.Dy() != 16 {
		t.Fatalf("scrubbed dimensions = %dx%d, want 8x16 (swapped, portrait)", b.Dx(), b.Dy())
	}

	// Source quadrant colors, each expected at the interior sample point its
	// 90°-CW rotation maps to (out_x = srcH-1-y, out_y = x, srcH=8).
	cases := []struct {
		name      string
		sampleX   int
		sampleY   int
		wantColor color.RGBA
	}{
		{"source TL (red) -> output top-right", 5, 2, color.RGBA{R: 220, G: 20, B: 20, A: 255}},
		{"source TR (green) -> output bottom-right", 5, 10, color.RGBA{R: 20, G: 200, B: 20, A: 255}},
		{"source BL (blue) -> output top-left", 1, 2, color.RGBA{R: 20, G: 20, B: 220, A: 255}},
		{"source BR (yellow) -> output bottom-left", 1, 10, color.RGBA{R: 220, G: 200, B: 20, A: 255}},
	}
	for _, c := range cases {
		pixel := img.At(b.Min.X+c.sampleX, b.Min.Y+c.sampleY)
		if !approxColorEqual(pixel, c.wantColor, colorTolerance) {
			t.Errorf("%s: pixel at (%d,%d) = %v, want ~%v", c.name, c.sampleX, c.sampleY, pixel, c.wantColor)
		}
	}

	if x, err := goexif.Decode(bytes.NewReader(got)); err == nil {
		if _, tagErr := x.Get(goexif.Orientation); tagErr == nil {
			t.Error("scrubbed output still carries an Orientation tag")
		}
	}
}

// TestScrubJPEGUprightIsLosslessStripOnly covers the "Orientation absent or
// 1" path: no pixel re-encode happens, so the scan data must come out
// byte-identical, with only the EXIF APP1 segment itself removed. This is
// checked exactly, not approximately: the expected output is computed
// directly by cutting the known APP1 segment out of the known input, and
// the scrubbed bytes must equal that computation exactly.
func TestScrubJPEGUprightIsLosslessStripOnly(t *testing.T) {
	base := quadrantJPEGBytes(t, 16, 8)
	tiff := buildEXIFBlob(t, 1, false) // Orientation=1, no GPS
	exifPayload := append([]byte("Exif\x00\x00"), tiff...)
	segment := jpegAPP1Segment(exifPayload)
	fixture := jpegWithEXIF(t, base, tiff)

	want := make([]byte, 0, len(fixture)-len(segment))
	want = append(want, 0xFF, 0xD8)
	want = append(want, base[2:]...)

	scrubber := adapter.NewExifScrubber()
	got, err := scrubber.Scrub(context.Background(), fixture, domain.ContentTypeJPEG)
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Scrub(orientation=1) did not produce an exact APP1-removed copy:\n got  %d bytes\n want %d bytes", len(got), len(want))
	}
}

// TestScrubJPEGWithNoEXIFLeavesBytesUnchanged covers the "no Orientation tag
// at all" branch of readJPEGOrientation (a JPEG with no EXIF APP1 segment
// whatsoever, as opposed to one with an explicit Orientation=1) via a real
// standard-library-encoded JPEG — proving the segment walk correctly passes
// through a normal JFIF/DQT/SOF0/DHT/SOS/entropy-data structure with nothing
// to strip.
func TestScrubJPEGWithNoEXIFLeavesBytesUnchanged(t *testing.T) {
	base := quadrantJPEGBytes(t, 16, 8) // no EXIF segment at all

	scrubber := adapter.NewExifScrubber()
	got, err := scrubber.Scrub(context.Background(), base, domain.ContentTypeJPEG)
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if !bytes.Equal(got, base) {
		t.Fatal("Scrub of a JPEG with no EXIF at all changed the bytes")
	}
}

// pngChunk is one parsed PNG chunk: its 4-character type and payload.
type pngChunk struct {
	typ  string
	data []byte
}

// parsePNGChunks walks data's chunk stream (after the 8-byte signature),
// used both to splice extra ancillary chunks into a base PNG and to inspect
// which chunk types survive scrubbing.
func parsePNGChunks(t *testing.T, data []byte) []pngChunk {
	t.Helper()
	var chunks []pngChunk
	pos := 8
	for pos < len(data) {
		if pos+8 > len(data) {
			t.Fatalf("parsePNGChunks: truncated chunk header at offset %d", pos)
		}
		length := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		typ := string(data[pos+4 : pos+8])
		payloadStart := pos + 8
		if payloadStart+length+4 > len(data) {
			t.Fatalf("parsePNGChunks: chunk %q claims %d bytes past end of data", typ, length)
		}
		payload := append([]byte(nil), data[payloadStart:payloadStart+length]...)
		chunks = append(chunks, pngChunk{typ: typ, data: payload})
		pos = payloadStart + length + 4 // skip the trailing CRC
	}
	return chunks
}

// pngWithAncillaryChunks builds a real, decodable 2x2 PNG (via png.Encode)
// and splices an eXIf and a tEXt chunk in right after IHDR — the ancillary
// metadata TestScrubPNGDropsAncillaryChunks proves gets dropped.
func pngWithAncillaryChunks(t *testing.T, base []byte) []byte {
	t.Helper()
	chunks := parsePNGChunks(t, base)
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	for _, c := range chunks {
		writePNGChunk(&buf, c.typ, c.data)
		if c.typ == "IHDR" {
			writePNGChunk(&buf, "eXIf", []byte("fake-tiff-payload-not-real-exif"))
			writePNGChunk(&buf, "tEXt", []byte("Comment\x00uploaded from a phone"))
		}
	}
	return buf.Bytes()
}

// TestScrubPNGDropsAncillaryChunks proves scrubPNG's decode/re-encode round
// trip is lossless for pixels but drops every ancillary chunk: only
// IHDR/PLTE/tRNS/IDAT/IEND may remain, matching image/png's writer, which
// never emits anything else.
func TestScrubPNGDropsAncillaryChunks(t *testing.T) {
	basePNG := pngBytes(t)
	fixture := pngWithAncillaryChunks(t, basePNG)

	// Sanity-check the fixture actually carries the ancillary chunks the
	// test means to prove get dropped.
	fixtureTypes := map[string]bool{}
	for _, c := range parsePNGChunks(t, fixture) {
		fixtureTypes[c.typ] = true
	}
	if !fixtureTypes["eXIf"] || !fixtureTypes["tEXt"] {
		t.Fatalf("fixture sanity check: missing eXIf/tEXt chunks, got %v", fixtureTypes)
	}

	scrubber := adapter.NewExifScrubber()
	got, err := scrubber.Scrub(context.Background(), fixture, domain.ContentTypePNG)
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}

	allowed := map[string]bool{"IHDR": true, "PLTE": true, "tRNS": true, "IDAT": true, "IEND": true}
	for _, c := range parsePNGChunks(t, got) {
		if !allowed[c.typ] {
			t.Errorf("scrubbed PNG still carries chunk %q", c.typ)
		}
	}

	wantImg, err := png.Decode(bytes.NewReader(basePNG))
	if err != nil {
		t.Fatalf("decode original: %v", err)
	}
	gotImg, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("decode scrubbed: %v", err)
	}
	b := wantImg.Bounds()
	if gotImg.Bounds() != b {
		t.Fatalf("scrubbed PNG bounds = %v, want %v", gotImg.Bounds(), b)
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if gotImg.At(x, y) != wantImg.At(x, y) {
				t.Fatalf("scrubbed pixel at (%d,%d) = %v, want %v (PNG re-encode must be lossless)", x, y, gotImg.At(x, y), wantImg.At(x, y))
			}
		}
	}
}

// TestScrubRejectsCorruptImage covers stripJPEGExifSegments' hardened walk:
// every one of these malformed inputs must be rejected with ErrInvalidPhoto
// rather than partially processed or passed through.
func TestScrubRejectsCorruptImage(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"does not start with SOI", []byte{0x00, 0x01, 0x02, 0x03}},
		{"SOI only, nothing after", []byte{0xFF, 0xD8}},
		{"truncated segment length field", []byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00}},
		{"segment length exceeds remaining data", []byte{0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF, 0x00, 0x00}},
	}
	scrubber := adapter.NewExifScrubber()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := scrubber.Scrub(context.Background(), c.data, domain.ContentTypeJPEG)
			if !errors.Is(err, domain.ErrInvalidPhoto) {
				t.Fatalf("Scrub(%s) = %v, want ErrInvalidPhoto", c.name, err)
			}
		})
	}
}

// TestScrubRejectsTruncatedSegment is the ticket's explicitly named case: a
// segment whose declared length runs past the end of the buffer.
func TestScrubRejectsTruncatedSegment(t *testing.T) {
	// FFD8 + FFE1 + length=0x0064 (100, far more than the 4 bytes that
	// follow) + a short, real-looking payload prefix.
	data := []byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x64, 'E', 'x', 'i', 'f'}
	scrubber := adapter.NewExifScrubber()
	if _, err := scrubber.Scrub(context.Background(), data, domain.ContentTypeJPEG); !errors.Is(err, domain.ErrInvalidPhoto) {
		t.Fatalf("Scrub(truncated segment) = %v, want ErrInvalidPhoto", err)
	}
}

// TestScrubRejectsJunkBytesBeforeEXIFSegment is the regression case the
// ticket calls out by name: a naive implementation that searches for the
// "Exif\0\0" signature anywhere in the byte stream (rather than walking
// well-formed marker segments) can be fooled by junk bytes placed ahead of a
// real EXIF segment into leaking it through untouched. This scrubber has no
// such search — the first byte after SOI that is not a valid marker aborts
// the whole scrub with ErrInvalidPhoto, so the junk can never hide a real
// segment from it.
func TestScrubRejectsJunkBytesBeforeEXIFSegment(t *testing.T) {
	tiff := buildEXIFBlob(t, 0, true)
	exifPayload := append([]byte("Exif\x00\x00"), tiff...)
	realEXIFSegment := jpegAPP1Segment(exifPayload)

	data := make([]byte, 0, 2+3+len(realEXIFSegment))
	data = append(data, 0xFF, 0xD8)       // SOI
	data = append(data, 0x00, 0x11, 0x22) // junk: not a marker
	data = append(data, realEXIFSegment...)

	scrubber := adapter.NewExifScrubber()
	_, err := scrubber.Scrub(context.Background(), data, domain.ContentTypeJPEG)
	if !errors.Is(err, domain.ErrInvalidPhoto) {
		t.Fatalf("Scrub(junk before real EXIF) = %v, want ErrInvalidPhoto", err)
	}
}

// TestScrubRejectsDecompressionBomb proves decodeUpright's dimension guard
// runs before any full decode allocates. Reused from testdata_test.go's
// decompressionBombPNGBytes: decodeUpright's maxScrubPixels check is the one
// code path both the JPEG-rotate and the PNG scrub paths share, so exercising
// it via PNG (which needs no hand-rolled fake JPEG SOF0 segment) covers the
// same guard.
func TestScrubRejectsDecompressionBomb(t *testing.T) {
	data := decompressionBombPNGBytes(t, 40000, 40000) // 1.6B pixels
	scrubber := adapter.NewExifScrubber()
	if _, err := scrubber.Scrub(context.Background(), data, domain.ContentTypePNG); !errors.Is(err, domain.ErrInvalidPhoto) {
		t.Fatalf("Scrub(decompression bomb) = %v, want ErrInvalidPhoto", err)
	}
}

// TestScrubUnsupportedContentTypeRejected covers Scrub's defensive default
// branch: PhotoValidator's sniff should already have rejected anything but
// JPEG/PNG, but Scrub does not trust that alone.
func TestScrubUnsupportedContentTypeRejected(t *testing.T) {
	scrubber := adapter.NewExifScrubber()
	_, err := scrubber.Scrub(context.Background(), []byte("whatever"), "image/gif")
	if !errors.Is(err, domain.ErrInvalidPhoto) {
		t.Fatalf("Scrub(unsupported content type) = %v, want ErrInvalidPhoto", err)
	}
}

// TestScrubBakesEveryOrientationUpright exercises all seven EXIF
// orientation values with a real correction (2-8; TestScrubBakesOrientationUpright
// already covers 6 end-to-end alongside GPS removal). For each, it tracks
// where the source's top-left red pixel at (2,2) must land — derived from
// this ticket's own coordinate formulas — and checks both the resulting
// image dimensions and that exact pixel's color, so every rotation/mirror
// branch in applyOrientation is verified against a real JPEG round trip, not
// just asserted mathematically in a comment.
func TestScrubBakesEveryOrientationUpright(t *testing.T) {
	base := quadrantJPEGBytes(t, 16, 8) // srcW=16, srcH=8
	red := color.RGBA{R: 220, G: 20, B: 20, A: 255}

	cases := []struct {
		orientation      int
		wantW, wantH     int
		sampleX, sampleY int
	}{
		{2, 16, 8, 13, 2}, // mirror horizontal
		{3, 16, 8, 13, 5}, // rotate 180
		{4, 16, 8, 2, 5},  // mirror vertical
		{5, 8, 16, 2, 2},  // transpose
		{6, 8, 16, 5, 2},  // rotate 90 CW
		{7, 8, 16, 5, 13}, // transverse
		{8, 8, 16, 2, 13}, // rotate 270 CW
	}
	scrubber := adapter.NewExifScrubber()
	for _, c := range cases {
		t.Run(fmt.Sprintf("orientation=%d", c.orientation), func(t *testing.T) {
			tiff := buildEXIFBlob(t, c.orientation, false)
			fixture := jpegWithEXIF(t, base, tiff)

			got, err := scrubber.Scrub(context.Background(), fixture, domain.ContentTypeJPEG)
			if err != nil {
				t.Fatalf("Scrub: %v", err)
			}
			img, _, err := image.Decode(bytes.NewReader(got))
			if err != nil {
				t.Fatalf("decode scrubbed output: %v", err)
			}
			b := img.Bounds()
			if b.Dx() != c.wantW || b.Dy() != c.wantH {
				t.Fatalf("dimensions = %dx%d, want %dx%d", b.Dx(), b.Dy(), c.wantW, c.wantH)
			}
			pixel := img.At(b.Min.X+c.sampleX, b.Min.Y+c.sampleY)
			if !approxColorEqual(pixel, red, colorTolerance) {
				t.Errorf("pixel at (%d,%d) = %v, want ~red %v", c.sampleX, c.sampleY, pixel, red)
			}
		})
	}
}

// TestScrubJPEGOutOfRangeOrientationTreatedAsUpright covers
// readJPEGOrientation's "err != nil || v < 1 || v > 8" guard: a tag value
// outside the eight defined EXIF orientations (a malformed or nonstandard
// camera write) must not confuse the scrubber into rotating anything — it
// falls back to the same lossless strip-only path Orientation absent/1 gets.
func TestScrubJPEGOutOfRangeOrientationTreatedAsUpright(t *testing.T) {
	base := quadrantJPEGBytes(t, 16, 8)
	tiff := buildEXIFBlob(t, 9, false) // 9 is not a defined EXIF orientation
	fixture := jpegWithEXIF(t, base, tiff)

	want := make([]byte, 0, len(fixture))
	want = append(want, 0xFF, 0xD8)
	want = append(want, base[2:]...)

	scrubber := adapter.NewExifScrubber()
	got, err := scrubber.Scrub(context.Background(), fixture, domain.ContentTypeJPEG)
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("Scrub(out-of-range orientation) did not fall back to a lossless strip-only copy")
	}
}

// TestScrubJPEGPassesThroughStandaloneMarkers covers stripJPEGExifSegments'
// standalone-marker branch (TEM, RSTn — no length field) and readMarker's
// fill-byte skip (a marker prefixed by one or more extra 0xFF padding
// bytes, which the JPEG spec permits): SOI, a fill-byte-padded restart
// marker, a TEM marker, then EOI must all pass through byte-for-byte.
func TestScrubJPEGPassesThroughStandaloneMarkers(t *testing.T) {
	data := []byte{
		0xFF, 0xD8, // SOI
		0xFF, 0xFF, 0xD0, // fill byte + RST0
		0xFF, 0x01, // TEM
		0xFF, 0xD9, // EOI
	}
	scrubber := adapter.NewExifScrubber()
	got, err := scrubber.Scrub(context.Background(), data, domain.ContentTypeJPEG)
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Scrub(standalone markers) = % X, want unchanged % X", got, data)
	}
}

// TestScrubRejectsCorruptPNG covers decodeUpright's DecodeConfig failure
// branch via the PNG path — bytes that are not a PNG at all.
func TestScrubRejectsCorruptPNG(t *testing.T) {
	scrubber := adapter.NewExifScrubber()
	_, err := scrubber.Scrub(context.Background(), []byte("not a png file"), domain.ContentTypePNG)
	if !errors.Is(err, domain.ErrInvalidPhoto) {
		t.Fatalf("Scrub(corrupt png) = %v, want ErrInvalidPhoto", err)
	}
}

// TestScrubRejectsTruncatedPNG covers decodeUpright's full image.Decode
// failure branch: a valid IHDR (so the DecodeConfig/pixel-count guard
// passes) followed by truncated IDAT data, which only a full decode
// detects.
func TestScrubRejectsTruncatedPNG(t *testing.T) {
	base := pngBytes(t)
	truncated := base[:len(base)-20]

	scrubber := adapter.NewExifScrubber()
	_, err := scrubber.Scrub(context.Background(), truncated, domain.ContentTypePNG)
	if !errors.Is(err, domain.ErrInvalidPhoto) {
		t.Fatalf("Scrub(truncated png) = %v, want ErrInvalidPhoto", err)
	}
}

// TestScrubRejectsContentTypeMismatch covers decodeUpright's defensive
// cross-check that the bytes actually decode as contentType claims: real
// JPEG bytes scrubbed as if they were PNG (or vice versa) must be rejected
// rather than silently processed as whatever they actually are.
func TestScrubRejectsContentTypeMismatch(t *testing.T) {
	realJPEG := quadrantJPEGBytes(t, 16, 8)
	tiff := buildEXIFBlob(t, 6, false) // orientation 2-8 forces the decodeUpright path
	fixture := jpegWithEXIF(t, realJPEG, tiff)

	scrubber := adapter.NewExifScrubber()
	_, err := scrubber.Scrub(context.Background(), fixture, domain.ContentTypePNG)
	if !errors.Is(err, domain.ErrInvalidPhoto) {
		t.Fatalf("Scrub(jpeg bytes declared as png) = %v, want ErrInvalidPhoto", err)
	}
}
