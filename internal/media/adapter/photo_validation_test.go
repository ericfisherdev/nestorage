package adapter_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/media/adapter"
	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

// TestPhotoValidator_ValidateAndStage_RenamedTextRejected covers a renamed
// .txt: ValidateAndStage never looks at a filename or client-declared type,
// only the sniffed bytes, so plain text is rejected regardless of what
// extension or Content-Type a client might have sent it under.
func TestPhotoValidator_ValidateAndStage_RenamedTextRejected(t *testing.T) {
	v := adapter.NewPhotoValidator(t.TempDir())
	_, err := v.ValidateAndStage(context.Background(), bytes.NewReader([]byte("this is not an image, just plain text")), 10<<20)
	if !errors.Is(err, domain.ErrUnsupportedMediaType) {
		t.Fatalf("ValidateAndStage(renamed text) = %v, want ErrUnsupportedMediaType", err)
	}
}

// TestPhotoValidator_ValidateAndStage_SniffsAsImageButUndecodable proves
// the image.DecodeConfig cross-check catches bytes whose magic prefix lies
// about a valid structure following it.
func TestPhotoValidator_ValidateAndStage_SniffsAsImageButUndecodable(t *testing.T) {
	v := adapter.NewPhotoValidator(t.TempDir())
	// Sniffs as image/jpeg (the magic prefix http.DetectContentType keys on)
	// but has no valid JPEG structure beyond that.
	sniffsAsJPEGButUndecodable := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01}
	_, err := v.ValidateAndStage(context.Background(), bytes.NewReader(sniffsAsJPEGButUndecodable), 10<<20)
	if !errors.Is(err, domain.ErrInvalidPhoto) {
		t.Fatalf("ValidateAndStage(undecodable) = %v, want ErrInvalidPhoto", err)
	}
}

func TestPhotoValidator_ValidateAndStage_ValidJPEGAndPNG(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"jpeg", jpegBytes(t), domain.ContentTypeJPEG},
		{"png", pngBytes(t), domain.ContentTypePNG},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := adapter.NewPhotoValidator(t.TempDir())
			staged, err := v.ValidateAndStage(context.Background(), bytes.NewReader(tc.data), 10<<20)
			if err != nil {
				t.Fatalf("ValidateAndStage: %v", err)
			}
			if staged.ContentType != tc.want {
				t.Errorf("ContentType = %q, want %q", staged.ContentType, tc.want)
			}
			if staged.SizeBytes != int64(len(tc.data)) {
				t.Errorf("SizeBytes = %d, want %d", staged.SizeBytes, len(tc.data))
			}
			if staged.Path == "" {
				t.Error("Path is empty")
			}
		})
	}
}

func TestPhotoValidator_ValidateAndStage_OversizeRejected(t *testing.T) {
	v := adapter.NewPhotoValidator(t.TempDir())
	data := pngBytes(t)
	_, err := v.ValidateAndStage(context.Background(), bytes.NewReader(data), 8)
	if !errors.Is(err, domain.ErrPhotoTooLarge) {
		t.Fatalf("ValidateAndStage(oversize) = %v, want ErrPhotoTooLarge", err)
	}
}

// TestPhotoValidator_ValidateAndStage_DecompressionBombRejected proves the
// width*height guard rejects a PNG claiming implausible dimensions BEFORE
// attempting the full decode that would allocate a pixel buffer sized to
// them — see decompressionBombPNGBytes's own doc for why no real
// 50-megapixel image ever needs to be encoded to exercise this.
func TestPhotoValidator_ValidateAndStage_DecompressionBombRejected(t *testing.T) {
	v := adapter.NewPhotoValidator(t.TempDir())
	// 10000 x 5001 = 50,010,000 pixels, just over the 50,000,000 limit.
	data := decompressionBombPNGBytes(t, 10000, 5001)
	_, err := v.ValidateAndStage(context.Background(), bytes.NewReader(data), 10<<20)
	if !errors.Is(err, domain.ErrInvalidPhoto) {
		t.Fatalf("ValidateAndStage(decompression bomb) = %v, want ErrInvalidPhoto", err)
	}
}

// TestPhotoValidator_ValidateAndStage_RejectionLeavesStagingEmpty proves
// every rejection path removes its own staging file, not just the
// over-cap one already covered by TestPhotoValidator_OverCapRejectedWithoutReadingWhole
// (upload_streaming_test.go).
func TestPhotoValidator_ValidateAndStage_RejectionLeavesStagingEmpty(t *testing.T) {
	dir := t.TempDir()
	v := adapter.NewPhotoValidator(dir)
	sniffsAsJPEGButUndecodable := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01}
	if _, err := v.ValidateAndStage(context.Background(), bytes.NewReader(sniffsAsJPEGButUndecodable), 10<<20); !errors.Is(err, domain.ErrInvalidPhoto) {
		t.Fatalf("ValidateAndStage(undecodable) = %v, want ErrInvalidPhoto", err)
	}

	entries, err := readDirNames(dir)
	if err != nil {
		t.Fatalf("read staging dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected ValidateAndStage left files behind in the staging directory: %v", entries)
	}
}
