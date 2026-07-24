package adapter

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"

	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

// thumbJPEGEncodeQuality is the quality ImageThumbnailer's JPEG output
// uses — distinct from ExifScrubber's jpegEncodeQuality (a different
// re-encode with a different fidelity/size tradeoff: the scrub path
// preserves near-original quality, the thumbnail path favors the smallest
// reasonable LAN payload).
const thumbJPEGEncodeQuality = 80

// ImageThumbnailer is the domain.PhotoThumbnailer implementation (NSTR-84):
// JPEG and PNG only, matching the accept-list every other media adapter
// shares. Stateless aside from maxEdge — safe for concurrent use.
type ImageThumbnailer struct {
	maxEdge int
}

// NewImageThumbnailer returns an ImageThumbnailer bounding its output to
// maxEdge pixels on the long edge, rejecting a non-positive edge — the
// fail-fast constructor convention every adapter in this package follows
// (see NewLocalPhotoStore).
func NewImageThumbnailer(maxEdge int) (*ImageThumbnailer, error) {
	if maxEdge <= 0 {
		return nil, fmt.Errorf("media/adapter: thumbnail max edge must be positive, got %d", maxEdge)
	}
	return &ImageThumbnailer{maxEdge: maxEdge}, nil
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.PhotoThumbnailer = (*ImageThumbnailer)(nil)

// Thumbnail decodes data via decodeUpright (exif_scrub.go) — reused rather
// than duplicated: that helper already guards the decompression-bomb bound
// and cross-checks the decoded format against contentType, and is safe to
// call on already-scrubbed bytes because a scrubbed JPEG carries no
// orientation tag left to bake in a second time (see decodeUpright's own
// doc, which reserves exactly this reuse for NSTR-84). It then scales the
// decoded image into t.maxEdge's bounding box with draw.CatmullRom (never
// upscaling — see fitWithin) and re-encodes at the SAME content type it
// decoded, so the serve path needs no variant-specific Content-Type logic
// and PNG transparency survives the round trip.
func (t *ImageThumbnailer) Thumbnail(data []byte, contentType string) (domain.ThumbResult, error) {
	if contentType != domain.ContentTypeJPEG && contentType != domain.ContentTypePNG {
		return domain.ThumbResult{}, fmt.Errorf("%w: thumbnailer does not support content type %q", domain.ErrUnsupportedMediaType, contentType)
	}

	src, err := decodeUpright(data, contentType)
	if err != nil {
		return domain.ThumbResult{}, err
	}

	srcBounds := src.Bounds()
	dstW, dstH := fitWithin(srcBounds.Dx(), srcBounds.Dy(), t.maxEdge)
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, srcBounds, draw.Src, nil)

	encoded, err := encodeThumb(dst, contentType)
	if err != nil {
		return domain.ThumbResult{}, err
	}
	return domain.ThumbResult{Bytes: encoded, ContentType: contentType, Width: dstW, Height: dstH}, nil
}

// fitWithin returns the dimensions a w-by-h image scales to so its long edge
// fits maxEdge, aspect ratio preserved, never upscaling: an image already
// inside the box is returned at its own, unchanged size (domain.
// PhotoThumbnailer's own doc — the caller re-encodes it anyway, so every
// photo still ends up with a thumb object).
func fitWithin(w, h, maxEdge int) (int, int) {
	if w <= maxEdge && h <= maxEdge {
		return w, h
	}
	if w >= h {
		return maxEdge, max(1, int(float64(h)*float64(maxEdge)/float64(w)))
	}
	return max(1, int(float64(w)*float64(maxEdge)/float64(h))), maxEdge
}

// encodeThumb re-encodes img at contentType — jpeg.Encode
// (thumbJPEGEncodeQuality) for ContentTypeJPEG, png.Encode (lossless,
// preserving alpha) for ContentTypePNG. contentType is already known to be
// one of these two (Thumbnail's own guard runs first).
func encodeThumb(img image.Image, contentType string) ([]byte, error) {
	var buf bytes.Buffer
	var err error
	switch contentType {
	case domain.ContentTypeJPEG:
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: thumbJPEGEncodeQuality})
	default: // domain.ContentTypePNG, the only other value Thumbnail's guard allows through
		err = png.Encode(&buf, img)
	}
	if err != nil {
		return nil, fmt.Errorf("media/adapter: encode thumbnail: %w", err)
	}
	return buf.Bytes(), nil
}
