package adapter_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/media/adapter"
	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

const testMaxEdge = 400

func newTestThumbnailer(t *testing.T) *adapter.ImageThumbnailer {
	t.Helper()
	th, err := adapter.NewImageThumbnailer(testMaxEdge)
	if err != nil {
		t.Fatalf("NewImageThumbnailer: %v", err)
	}
	return th
}

// largePNGBytesWithAlpha builds a w-by-h PNG carrying a partially
// transparent pixel, so TestImageThumbnailer_PNGAlphaPreserved has
// something to assert survives the round trip.
func largePNGBytesWithAlpha(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x % 256), G: uint8(y % 256), B: 100, A: 128})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func TestImageThumbnailer_LandscapeFitsLongEdge(t *testing.T) {
	th := newTestThumbnailer(t)
	data := largeJPEGBytes(t, 1600, 900) // 16:9 landscape
	result, err := th.Thumbnail(data, domain.ContentTypeJPEG)
	if err != nil {
		t.Fatalf("Thumbnail: %v", err)
	}
	if result.Width != testMaxEdge {
		t.Errorf("Width = %d, want %d (the long edge)", result.Width, testMaxEdge)
	}
	wantHeight := 900 * testMaxEdge / 1600
	if result.Height != wantHeight {
		t.Errorf("Height = %d, want %d (aspect ratio preserved)", result.Height, wantHeight)
	}
}

func TestImageThumbnailer_PortraitFitsLongEdge(t *testing.T) {
	th := newTestThumbnailer(t)
	data := largeJPEGBytes(t, 900, 1600) // portrait
	result, err := th.Thumbnail(data, domain.ContentTypeJPEG)
	if err != nil {
		t.Fatalf("Thumbnail: %v", err)
	}
	if result.Height != testMaxEdge {
		t.Errorf("Height = %d, want %d (the long edge)", result.Height, testMaxEdge)
	}
	wantWidth := 900 * testMaxEdge / 1600
	if result.Width != wantWidth {
		t.Errorf("Width = %d, want %d (aspect ratio preserved)", result.Width, wantWidth)
	}
}

// TestImageThumbnailer_SmallerThanBoxNotUpscaled proves an image already
// inside the bounding box is re-encoded at its own size rather than
// upscaled.
func TestImageThumbnailer_SmallerThanBoxNotUpscaled(t *testing.T) {
	th := newTestThumbnailer(t)
	data := largeJPEGBytes(t, 200, 100)
	result, err := th.Thumbnail(data, domain.ContentTypeJPEG)
	if err != nil {
		t.Fatalf("Thumbnail: %v", err)
	}
	if result.Width != 200 || result.Height != 100 {
		t.Errorf("dimensions = %dx%d, want the original 200x100 (no upscale)", result.Width, result.Height)
	}
}

func TestImageThumbnailer_JPEGInJPEGOut(t *testing.T) {
	th := newTestThumbnailer(t)
	data := largeJPEGBytes(t, 600, 600)
	result, err := th.Thumbnail(data, domain.ContentTypeJPEG)
	if err != nil {
		t.Fatalf("Thumbnail: %v", err)
	}
	if result.ContentType != domain.ContentTypeJPEG {
		t.Errorf("ContentType = %q, want %q", result.ContentType, domain.ContentTypeJPEG)
	}
	if _, err := jpeg.Decode(bytes.NewReader(result.Bytes)); err != nil {
		t.Errorf("result.Bytes does not decode as JPEG: %v", err)
	}
}

func TestImageThumbnailer_PNGInPNGOut(t *testing.T) {
	th := newTestThumbnailer(t)
	data := largePNGBytesWithAlpha(t, 600, 600)
	result, err := th.Thumbnail(data, domain.ContentTypePNG)
	if err != nil {
		t.Fatalf("Thumbnail: %v", err)
	}
	if result.ContentType != domain.ContentTypePNG {
		t.Errorf("ContentType = %q, want %q", result.ContentType, domain.ContentTypePNG)
	}
	if _, err := png.Decode(bytes.NewReader(result.Bytes)); err != nil {
		t.Errorf("result.Bytes does not decode as PNG: %v", err)
	}
}

// TestImageThumbnailer_PNGAlphaPreserved proves the alpha channel survives
// the decode-scale-re-encode round trip: the thumbnail's corner pixel must
// still be partially transparent, not opaque.
func TestImageThumbnailer_PNGAlphaPreserved(t *testing.T) {
	th := newTestThumbnailer(t)
	data := largePNGBytesWithAlpha(t, 600, 600)
	result, err := th.Thumbnail(data, domain.ContentTypePNG)
	if err != nil {
		t.Fatalf("Thumbnail: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(result.Bytes))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	_, _, _, a := img.At(img.Bounds().Min.X, img.Bounds().Min.Y).RGBA()
	if a == 0xffff {
		t.Error("thumbnail's corner pixel is fully opaque, want the source's partial transparency preserved")
	}
}

func TestImageThumbnailer_NonImageBytesRejected(t *testing.T) {
	th := newTestThumbnailer(t)
	if _, err := th.Thumbnail([]byte("not an image"), domain.ContentTypeJPEG); !errors.Is(err, domain.ErrInvalidPhoto) {
		t.Fatalf("Thumbnail(garbage) = %v, want ErrInvalidPhoto", err)
	}
}

func TestImageThumbnailer_UnsupportedContentTypeRejected(t *testing.T) {
	th := newTestThumbnailer(t)
	data := largeJPEGBytes(t, 100, 100)
	if _, err := th.Thumbnail(data, "image/gif"); !errors.Is(err, domain.ErrUnsupportedMediaType) {
		t.Fatalf("Thumbnail(image/gif) = %v, want ErrUnsupportedMediaType", err)
	}
}

// TestImageThumbnailer_DecompressionBombRejectedBeforeDecode proves the
// claimed-dimensions guard (reused from decodeUpright) rejects a small file
// claiming enormous dimensions before ever allocating a decode buffer sized
// to them.
func TestImageThumbnailer_DecompressionBombRejectedBeforeDecode(t *testing.T) {
	th := newTestThumbnailer(t)
	data := decompressionBombPNGBytes(t, 20_000, 20_000) // 400,000,000 claimed pixels
	if _, err := th.Thumbnail(data, domain.ContentTypePNG); !errors.Is(err, domain.ErrInvalidPhoto) {
		t.Fatalf("Thumbnail(decompression bomb) = %v, want ErrInvalidPhoto", err)
	}
}

func TestNewImageThumbnailer_NonPositiveMaxEdgeRejected(t *testing.T) {
	if _, err := adapter.NewImageThumbnailer(0); err == nil {
		t.Error("NewImageThumbnailer(0) error = nil, want an error")
	}
	if _, err := adapter.NewImageThumbnailer(-1); err == nil {
		t.Error("NewImageThumbnailer(-1) error = nil, want an error")
	}
}
