package adapter_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/media/adapter"
	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

// maxChunkReader tracks the largest single read buffer the caller ever
// requested (len(p), not the bytes actually returned), so a test can assert
// the caller never tried to slurp the whole source in one shot.
type maxChunkReader struct {
	r            io.Reader
	maxRequested int
}

func (m *maxChunkReader) Read(p []byte) (int, error) {
	if len(p) > m.maxRequested {
		m.maxRequested = len(p)
	}
	return m.r.Read(p)
}

// TestPhotoValidator_OverCapRejectedWithoutReadingWhole covers Sprint 5
// reconciliation R2's rewritten upload_streaming_test.go contract: an
// over-cap body is rejected without ValidateAndStage ever reading it whole,
// and the staging directory is left empty afterward.
func TestPhotoValidator_OverCapRejectedWithoutReadingWhole(t *testing.T) {
	dir := t.TempDir()
	v := adapter.NewPhotoValidator(dir)
	big := largeJPEGBytes(t, 900, 900)
	tracker := &maxChunkReader{r: bytes.NewReader(big)}

	_, err := v.ValidateAndStage(context.Background(), tracker, 8)
	if !errors.Is(err, domain.ErrPhotoTooLarge) {
		t.Fatalf("ValidateAndStage(oversize) = %v, want ErrPhotoTooLarge", err)
	}
	// io.Copy's default internal buffer is 32 KiB; allow headroom for the
	// sniff read without permitting anything close to len(big).
	const maxReasonableChunk = 128 << 10
	if tracker.maxRequested > maxReasonableChunk {
		t.Fatalf("ValidateAndStage requested a %d-byte read buffer (upload was %d bytes); it must stream in bounded chunks, not buffer the whole upload", tracker.maxRequested, len(big))
	}

	var files []string
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk staging dir: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("rejected upload left files behind in the staging directory: %v", files)
	}
}
