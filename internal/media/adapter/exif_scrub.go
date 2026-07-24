package adapter

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"

	"github.com/xor-gate/goexif2/exif"

	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

// jpegEncodeQuality is the quality ExifScrubber's JPEG re-encode path
// (Orientation 2-8) uses. Go's jpeg.Encode writes no metadata segments at
// all, so re-encoding strips every EXIF/XMP/comment segment as a side
// effect of baking in the orientation correction.
const jpegEncodeQuality = 90

// maxScrubPixels bounds width*height before ExifScrubber fully decodes an
// image (the rotated-JPEG and PNG paths both need a full image.Image, not
// just the header) — the same decompression-bomb guard PhotoValidator
// applies at upload time (maxDecodePixels), reapplied here because
// PhotoScrubber must not trust that its caller already checked.
const maxScrubPixels = maxDecodePixels

// jpeg marker bytes this package's hardened segment walk (stripJPEGExifSegments)
// recognizes. Every other marker byte is treated generically as a
// length-prefixed segment (see readMarker/stripJPEGExifSegments).
const (
	jpegMarkerPrefix byte = 0xFF
	jpegSOI          byte = 0xD8
	jpegEOI          byte = 0xD9
	jpegSOS          byte = 0xDA
	jpegAPP1         byte = 0xE1
	jpegRST0         byte = 0xD0
	jpegRST7         byte = 0xD7
	jpegTEM          byte = 0x01
)

// exifAPP1Signature is the 6-byte prefix ("Exif\0\0", CIPA DC-008) that marks
// an APP1 segment as EXIF-carrying, as opposed to XMP or another use of
// APP1. Only segments beginning with this exact signature are dropped by
// stripJPEGExifSegments; any other APP1 payload is left alone.
var exifAPP1Signature = []byte("Exif\x00\x00")

// ExifScrubber is the domain.PhotoScrubber implementation (NSTR-36): JPEG
// and PNG only, matching the accept-list Sprint 5 reconciliation R4 narrowed
// PhotoValidator to. Stateless — safe for concurrent use.
type ExifScrubber struct{}

// NewExifScrubber returns an ExifScrubber.
func NewExifScrubber() *ExifScrubber { return &ExifScrubber{} }

// Compile-time assurance the adapter satisfies the port.
var _ domain.PhotoScrubber = (*ExifScrubber)(nil)

// Scrub dispatches to the JPEG or PNG scrub path by contentType, or rejects
// with ErrInvalidPhoto for anything else (see domain.PhotoScrubber's own
// doc). ctx is unused today — accepted only to keep this port's shape
// consistent with PhotoValidator.ValidateAndStage's.
func (ExifScrubber) Scrub(_ context.Context, data []byte, contentType string) ([]byte, error) {
	switch contentType {
	case domain.ContentTypeJPEG:
		return scrubJPEG(data)
	case domain.ContentTypePNG:
		return scrubPNG(data)
	default:
		return nil, fmt.Errorf("%w: scrubber does not support content type %q", domain.ErrInvalidPhoto, contentType)
	}
}

// scrubJPEG removes data's EXIF metadata. An image with no orientation tag,
// or Orientation 1 (already upright), is handled by a byte-level segment
// strip with no re-encode and no quality loss (stripJPEGExifSegments).
// Orientation 2-8 needs the pixels baked upright before the tag can be
// safely discarded, so that path fully decodes and re-encodes instead.
func scrubJPEG(data []byte) ([]byte, error) {
	if readJPEGOrientation(data) <= 1 {
		return stripJPEGExifSegments(data)
	}
	return reencodeUprightJPEG(data)
}

// scrubPNG removes data's ancillary chunks (eXIf, tEXt, iTXt, zTXt, ...) by
// decoding and re-encoding through the standard library, which is lossless
// for PNG and, per image/png's writer, only ever emits IHDR/PLTE/tRNS/IDAT/
// IEND. PNG carries no EXIF-style orientation tag, so there is no rotation
// to bake in here.
func scrubPNG(data []byte) ([]byte, error) {
	img, err := decodeUpright(data, domain.ContentTypePNG)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("media/adapter: re-encode png: %w", err)
	}
	return buf.Bytes(), nil
}

// reencodeUprightJPEG fully decodes data, bakes its EXIF orientation into
// the pixels, and re-encodes at jpegEncodeQuality — Go's encoder writes no
// metadata, so this strips EXIF as a side effect of straightening the image.
func reencodeUprightJPEG(data []byte) ([]byte, error) {
	img, err := decodeUpright(data, domain.ContentTypeJPEG)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegEncodeQuality}); err != nil {
		return nil, fmt.Errorf("media/adapter: re-encode upright jpeg: %w", err)
	}
	return buf.Bytes(), nil
}

// decodeUpright fully decodes data (already known, via contentType, to be
// JPEG or PNG) into an orientation-corrected image.Image, guarding the
// decode against a decompression bomb via maxScrubPixels first. JPEG
// orientation (EXIF tag 2-8) is baked into the returned pixels; PNG carries
// no such tag and decodes as-is.
//
// This is the one full-decode path Scrub needs (the no-rotation JPEG case
// never fully decodes — see stripJPEGExifSegments). NSTR-84 (thumbnail
// generation) should call this rather than decoding an upload a second time.
func decodeUpright(data []byte, contentType string) (image.Image, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: bytes are not a valid image: %v", domain.ErrInvalidPhoto, err)
	}
	if pixels := int64(cfg.Width) * int64(cfg.Height); pixels > maxScrubPixels {
		return nil, fmt.Errorf("%w: image is %dx%d (%d pixels), exceeds the %d-pixel limit", domain.ErrInvalidPhoto, cfg.Width, cfg.Height, pixels, maxScrubPixels)
	}
	img, decodedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: image data is truncated or corrupt: %v", domain.ErrInvalidPhoto, err)
	}
	if formatToType[decodedFormat] != contentType || formatToType[format] != contentType {
		return nil, fmt.Errorf("%w: decoded format %q does not match expected content type %q", domain.ErrInvalidPhoto, decodedFormat, contentType)
	}

	if contentType == domain.ContentTypeJPEG {
		if orientation := readJPEGOrientation(data); orientation > 1 {
			img = applyOrientation(img, orientation)
		}
	}
	return img, nil
}

// readJPEGOrientation returns data's EXIF Orientation tag value (1-8), or 0
// when no EXIF, no Orientation tag, or an out-of-range value is present —
// callers treat 0 exactly like 1 (already upright, nothing to bake in).
//
// Hardened with a recover guard: goexif2 parses untrusted upload bytes, and
// a panic deep in a malformed IFD must not crash the server — the safest
// fallback on a panic is "no orientation info," which routes scrubJPEG to
// the byte-level strip path rather than silently trusting a half-parsed
// rotation.
func readJPEGOrientation(data []byte) (orientation int) {
	defer func() {
		if recover() != nil {
			orientation = 0
		}
	}()
	x, err := exif.Decode(bytes.NewReader(data))
	if err != nil {
		return 0
	}
	tag, err := x.Get(exif.Orientation)
	if err != nil {
		return 0
	}
	v, err := tag.Int(0)
	if err != nil || v < 1 || v > 8 {
		return 0
	}
	return v
}

// stripJPEGExifSegments walks data's marker segments from the SOI, dropping
// every APP1 segment carrying exifAPP1Signature and copying every other byte
// through untouched — including the entropy-coded scan data after SOS, which
// this walk never parses (EXIF never appears there, and 0xFF bytes in scan
// data are stuffing or restart markers, not further segment boundaries).
//
// It REJECTS with a wrapped ErrInvalidPhoto at the first structurally
// inconsistent byte — a truncated length, a segment claiming more bytes than
// remain, or running out of input before ever reaching SOS/EOI — rather than
// falling back to copying the remaining unexamined bytes through. That
// pass-through fallback was Nestova's reference implementation's proven
// privacy hole: junk bytes ahead of a real EXIF segment could hide it from a
// naive byte search and let it leak to storage intact. This walk has no such
// fallback — every byte up to SOS must parse as a recognized marker.
func stripJPEGExifSegments(data []byte) ([]byte, error) {
	if len(data) < 2 || data[0] != jpegMarkerPrefix || data[1] != jpegSOI {
		return nil, fmt.Errorf("%w: jpeg does not start with SOI", domain.ErrInvalidPhoto)
	}
	out := make([]byte, 0, len(data))
	out = append(out, data[0], data[1])
	pos := 2

	for {
		markerStart := pos
		marker, ok := readMarker(data, &pos)
		if !ok {
			return nil, fmt.Errorf("%w: jpeg ended before SOS", domain.ErrInvalidPhoto)
		}
		// markerBytes is the literal bytes readMarker just consumed — the
		// mandatory 0xFF plus any 0xFF fill bytes the spec permits before the
		// marker code itself — copied verbatim rather than reconstructed, so
		// a real-world fill-byte-padded marker round-trips exactly.
		markerBytes := data[markerStart:pos]

		if marker == jpegTEM || (marker >= jpegRST0 && marker <= jpegRST7) {
			// Standalone markers: no length, no payload.
			out = append(out, markerBytes...)
			continue
		}
		if marker == jpegEOI {
			out = append(out, markerBytes...)
			return out, nil
		}

		// Every other marker (APPn, DQT, SOF, DHT, COM, DRI, SOS, ...)
		// carries a big-endian 2-byte length that includes itself.
		segStart := pos
		if segStart+2 > len(data) {
			return nil, fmt.Errorf("%w: truncated segment length", domain.ErrInvalidPhoto)
		}
		length := int(binary.BigEndian.Uint16(data[segStart : segStart+2]))
		if length < 2 || segStart+length > len(data) {
			return nil, fmt.Errorf("%w: segment length out of bounds", domain.ErrInvalidPhoto)
		}
		segment := data[segStart : segStart+length]
		pos = segStart + length

		isExifAPP1 := marker == jpegAPP1 && bytes.HasPrefix(segment[2:], exifAPP1Signature)
		if !isExifAPP1 {
			out = append(out, markerBytes...)
			out = append(out, segment...)
		}
		// else: drop — neither the marker, length, nor payload is copied.

		if marker == jpegSOS {
			out = append(out, data[pos:]...)
			return out, nil
		}
	}
}

// readMarker reads the next 0xFF-prefixed marker byte at *pos, skipping any
// 0xFF fill bytes the JPEG spec permits between markers, and advances *pos
// past it. Returns ok=false when data runs out first.
func readMarker(data []byte, pos *int) (marker byte, ok bool) {
	i := *pos
	if i >= len(data) || data[i] != jpegMarkerPrefix {
		return 0, false
	}
	i++
	for i < len(data) && data[i] == jpegMarkerPrefix {
		i++
	}
	if i >= len(data) {
		return 0, false
	}
	marker = data[i]
	*pos = i + 1
	return marker, true
}

// applyOrientation returns img transformed so that baking EXIF
// orientation o (2-8; 1/absent is the caller's "nothing to do" case) into
// the pixels makes the result display upright with no further rotation
// needed. See the EXIF/TIFF orientation tag table (CIPA DC-008) for the
// eight defined values; 5-8 swap width and height.
func applyOrientation(img image.Image, o int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	at := func(x, y int) (uint32, uint32, uint32, uint32) { return img.At(b.Min.X+x, b.Min.Y+y).RGBA() }

	switch o {
	case 2: // mirror horizontal
		out := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, g, bl, a := at(x, y)
				out.SetRGBA64(w-1-x, y, rgba64(r, g, bl, a))
			}
		}
		return out
	case 3: // rotate 180
		out := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, g, bl, a := at(x, y)
				out.SetRGBA64(w-1-x, h-1-y, rgba64(r, g, bl, a))
			}
		}
		return out
	case 4: // mirror vertical
		out := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, g, bl, a := at(x, y)
				out.SetRGBA64(x, h-1-y, rgba64(r, g, bl, a))
			}
		}
		return out
	case 5: // transpose: mirror horizontal + rotate 270 CW
		out := image.NewRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, g, bl, a := at(x, y)
				out.SetRGBA64(y, x, rgba64(r, g, bl, a))
			}
		}
		return out
	case 6: // rotate 90 CW
		out := image.NewRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, g, bl, a := at(x, y)
				out.SetRGBA64(h-1-y, x, rgba64(r, g, bl, a))
			}
		}
		return out
	case 7: // transverse: mirror horizontal + rotate 90 CW
		out := image.NewRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, g, bl, a := at(x, y)
				out.SetRGBA64(h-1-y, w-1-x, rgba64(r, g, bl, a))
			}
		}
		return out
	case 8: // rotate 270 CW (90 CCW)
		out := image.NewRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, g, bl, a := at(x, y)
				out.SetRGBA64(y, w-1-x, rgba64(r, g, bl, a))
			}
		}
		return out
	default:
		return img
	}
}

// rgba64 packs the four uint32 (16-bit-scale) channels image.Color.RGBA
// returns into a color.RGBA64, the type image.RGBA.SetRGBA64 expects.
func rgba64(r, g, b, a uint32) color.RGBA64 {
	return color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)}
}
