package adapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path"
	"strings"

	// Image format decoders registered for image.DecodeConfig/image.Decode:
	// jpeg/png from the standard library only. NOT golang.org/x/image/webp —
	// Sprint 5 reconciliation R3 drops webp from the accept-list entirely
	// (Go has no webp ENCODER, so NSTR-36's scrubber could never re-emit
	// one), which is what lets this ticket drop the x/image dependency too.
	_ "image/jpeg"
	_ "image/png"

	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"

	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

// acceptedTypes maps an accepted (server-sniffed) content type to the file
// extension used for the stored object. Anything else is rejected as
// ErrUnsupportedMediaType. Keys are domain.ContentType* — the same constants
// Photo.Validate checks against — so the accept-list has one source of
// truth across the domain and adapter.
var acceptedTypes = map[string]string{
	domain.ContentTypeJPEG: "jpg",
	domain.ContentTypePNG:  "png",
}

// formatToType maps an image.DecodeConfig format name back to its content
// type, so the sniffed type can be cross-checked against the actual decoded
// bytes.
var formatToType = map[string]string{
	"jpeg": domain.ContentTypeJPEG,
	"png":  domain.ContentTypePNG,
}

// extensionForContentType returns the stored-object file extension
// acceptedTypes maps contentType to, and ok=false when contentType is not
// one of the accepted image types — shared by ValidateAndStage's accept
// check and LocalPhotoStore.Put's key-building, so the two can never drift
// apart.
func extensionForContentType(contentType string) (ext string, ok bool) {
	ext, ok = acceptedTypes[contentType]
	return ext, ok
}

// sniffLen is how many leading bytes ValidateAndStage reads to determine an
// upload's true content type via http.DetectContentType — the client's
// declared Content-Type is never consulted, so a renamed or mislabeled file
// cannot slip past it.
const sniffLen = 512

// stagingTempFilePattern names the staging file ValidateAndStage streams an
// upload into.
const stagingTempFilePattern = "media-validate-*.tmp"

// maxDecodePixels bounds width*height for the full image.Decode below,
// checked against image.DecodeConfig's header-only dimensions before that
// decode allocates a full pixel buffer — a guard against a decompression
// bomb (a small file whose header claims enormous dimensions). 50
// megapixels comfortably covers real camera photos while bounding
// worst-case decode memory.
const maxDecodePixels = 50_000_000

// PhotoValidator is the domain.PhotoValidator implementation: the shared
// streaming validate-and-stage pipeline every PhotoStore backend's upload
// path relies on (NSTR-35 reuses it verbatim for its S3 adapter).
type PhotoValidator struct {
	// dir is the directory ValidateAndStage stages uploads into, passed
	// straight through to os.CreateTemp. Empty means the OS default temp
	// directory (os.CreateTemp's own "dir=\"\" means os.TempDir()"
	// contract) — the production default, since PhotoService is
	// storage-backend-agnostic and must not couple staging to any one
	// PhotoStore's root. Tests inject t.TempDir() so an assertion that the
	// staging directory is left empty after a rejection has somewhere
	// exclusive to look.
	dir string
}

// NewPhotoValidator returns a PhotoValidator staging uploads under dir
// (empty for the OS default temp directory).
func NewPhotoValidator(dir string) *PhotoValidator { return &PhotoValidator{dir: dir} }

// Compile-time assurance the adapter satisfies the port.
var _ domain.PhotoValidator = (*PhotoValidator)(nil)

// ValidateAndStage streams r into a new temp file under v.dir, applying the
// same validation every PhotoStore backend must guarantee regardless of
// where bytes ultimately land: sniffs the true content type from the first
// sniffLen bytes (never a caller-supplied claim), enforces maxUploadBytes
// while copying, and cross-validates that the bytes actually decode as the
// sniffed type (rejecting a corrupt/truncated payload or one whose magic
// bytes lied about its format).
//
// On any rejection the staging file is removed and the returned
// domain.StagedUpload is the zero value; on success the caller is
// responsible for removing StagedUpload.Path once done with it.
func (v PhotoValidator) ValidateAndStage(_ context.Context, r io.Reader, maxUploadBytes int64) (domain.StagedUpload, error) {
	// Caps total bytes read (sniff + copy) at maxUploadBytes+1: enough to
	// detect an oversize upload without ever buffering more than that in
	// flight.
	limited := io.LimitReader(r, maxUploadBytes+1)

	sniff := make([]byte, sniffLen)
	n, err := io.ReadFull(limited, sniff)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return domain.StagedUpload{}, fmt.Errorf("media/adapter: read upload: %w", err)
	}
	sniff = sniff[:n]
	sniffedType := canonicalType(http.DetectContentType(sniff))
	if _, ok := extensionForContentType(sniffedType); !ok {
		return domain.StagedUpload{}, fmt.Errorf("%w: %q", domain.ErrUnsupportedMediaType, sniffedType)
	}

	tmp, err := os.CreateTemp(v.dir, stagingTempFilePattern)
	if err != nil {
		return domain.StagedUpload{}, fmt.Errorf("media/adapter: create staging file: %w", err)
	}
	tmpPath := tmp.Name()
	staged := false
	defer func() {
		_ = tmp.Close()
		if !staged {
			_ = os.Remove(tmpPath)
		}
	}()

	// Replay the already-consumed sniff prefix ahead of whatever remains on
	// limited, so the full stream (not just the tail after sniffing) reaches
	// the staging file.
	written, err := io.Copy(tmp, io.MultiReader(bytes.NewReader(sniff), limited))
	if err != nil {
		return domain.StagedUpload{}, fmt.Errorf("media/adapter: write upload: %w", err)
	}
	if written > maxUploadBytes {
		return domain.StagedUpload{}, fmt.Errorf("%w: %d bytes exceeds the %d-byte limit", domain.ErrPhotoTooLarge, written, maxUploadBytes)
	}

	// The bytes must actually decode as the sniffed type; this rejects a
	// corrupt/empty payload or content whose magic bytes lied about its
	// format.
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return domain.StagedUpload{}, fmt.Errorf("media/adapter: seek staged upload: %w", err)
	}
	cfg, format, err := image.DecodeConfig(tmp)
	if err != nil || formatToType[format] != sniffedType {
		return domain.StagedUpload{}, fmt.Errorf("%w: bytes are not a valid %s image", domain.ErrInvalidPhoto, sniffedType)
	}
	// Check claimed dimensions before the full decode below allocates a
	// pixel buffer sized to them — a small file can still claim an enormous
	// width/height (a decompression bomb), and DecodeConfig alone never
	// allocates enough to reveal that.
	if pixels := int64(cfg.Width) * int64(cfg.Height); pixels > maxDecodePixels {
		return domain.StagedUpload{}, fmt.Errorf("%w: image is %dx%d (%d pixels), exceeds the %d-pixel limit", domain.ErrInvalidPhoto, cfg.Width, cfg.Height, pixels, maxDecodePixels)
	}
	// DecodeConfig only reads the header, so a file truncated partway
	// through its entropy-coded image data can still pass it; a full Decode
	// is the only way to catch that.
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return domain.StagedUpload{}, fmt.Errorf("media/adapter: seek staged upload: %w", err)
	}
	if _, _, err := image.Decode(tmp); err != nil {
		return domain.StagedUpload{}, fmt.Errorf("%w: image data is truncated or corrupt", domain.ErrInvalidPhoto)
	}

	staged = true
	return domain.StagedUpload{Path: tmpPath, ContentType: sniffedType, SizeBytes: written}, nil
}

// buildStorageKey builds the content-addressed, item-scoped key every
// PhotoStore backend uses — items/<item>/<hash>.<ext> — shared by
// LocalPhotoStore (where it becomes a relative filesystem path) and
// NSTR-35's S3 adapter (where it becomes an object key verbatim) so the key
// layout can never drift between backends. Built with "path".Join (forward
// slashes only, never the OS path separator) since an S3 key is never
// OS-path-shaped; LocalPhotoStore.resolve converts to the OS separator only
// at the point it actually touches the filesystem.
func buildStorageKey(itemID storagedomain.ItemID, hash, ext string) string {
	return path.Join("items", itemID.String(), hash+"."+ext)
}

// canonicalType lowercases a content type and strips any parameters (e.g.
// "image/jpeg; charset=binary" -> "image/jpeg").
func canonicalType(contentType string) string {
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return strings.ToLower(strings.TrimSpace(contentType))
}
