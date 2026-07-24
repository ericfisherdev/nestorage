package adapter_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"testing"
	"time"

	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"

	"github.com/ericfisherdev/nestorage/internal/media/adapter"
	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

func newLocalStoreFactory(maxBytes int64) func(t *testing.T) domain.PhotoStore {
	return func(t *testing.T) domain.PhotoStore {
		t.Helper()
		s, err := adapter.NewLocalPhotoStore(t.TempDir(), maxBytes)
		if err != nil {
			t.Fatalf("NewLocalPhotoStore: %v", err)
		}
		return s
	}
}

// TestLocalPhotoStore_Conformance runs the shared PhotoStore port suite
// against LocalPhotoStore — NSTR-35 runs the identical suite against its S3
// adapter.
func TestLocalPhotoStore_Conformance(t *testing.T) {
	RunPhotoStoreConformance(t, newLocalStoreFactory(10<<20))
}

// TestLocalPhotoStore_ResolveRejectsTraversal is local-only: no other
// backend has a filesystem root a ref could escape.
func TestLocalPhotoStore_ResolveRejectsTraversal(t *testing.T) {
	s, err := adapter.NewLocalPhotoStore(t.TempDir(), 10<<20)
	if err != nil {
		t.Fatalf("NewLocalPhotoStore: %v", err)
	}
	if _, err := s.Open(context.Background(), domain.StorageRef("../../etc/passwd")); !errors.Is(err, domain.ErrInvalidPhoto) {
		t.Fatalf("Open(traversal ref) = %v, want ErrInvalidPhoto", err)
	}
}

// TestLocalPhotoStore_RejectionLeavesStagingEmpty proves an oversize write
// (Put's own defense-in-depth cap — see NewLocalPhotoStore's doc) leaves no
// partial file behind in the store root.
func TestLocalPhotoStore_RejectionLeavesStagingEmpty(t *testing.T) {
	root := t.TempDir()
	s, err := adapter.NewLocalPhotoStore(root, 8)
	if err != nil {
		t.Fatalf("NewLocalPhotoStore: %v", err)
	}
	data := jpegBytes(t)
	meta := computeMeta(data, domain.ContentTypeJPEG)
	if _, err := s.Put(context.Background(), storagedomain.NewItemID(), meta, bytes.NewReader(data)); !errors.Is(err, domain.ErrPhotoTooLarge) {
		t.Fatalf("Put(oversize) = %v, want ErrPhotoTooLarge", err)
	}

	var files []string
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk store root: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("rejected Put left files behind in the store root: %v", files)
	}
}

func TestNewLocalPhotoStore_BlankRootRejected(t *testing.T) {
	if _, err := adapter.NewLocalPhotoStore("  ", 10<<20); err == nil {
		t.Error("NewLocalPhotoStore(blank root) error = nil, want an error")
	}
}

func TestNewLocalPhotoStore_NonPositiveMaxUploadBytesRejected(t *testing.T) {
	if _, err := adapter.NewLocalPhotoStore(t.TempDir(), 0); err == nil {
		t.Error("NewLocalPhotoStore(maxUploadBytes=0) error = nil, want an error")
	}
}

// TestLocalPhotoStore_PutUnsupportedContentTypeRejected proves Put itself
// still guards meta.ContentType (it builds the file extension from it),
// even though PhotoService's PhotoValidator is the layer that ordinarily
// stops an unsupported type from ever reaching Put.
func TestLocalPhotoStore_PutUnsupportedContentTypeRejected(t *testing.T) {
	s, err := adapter.NewLocalPhotoStore(t.TempDir(), 10<<20)
	if err != nil {
		t.Fatalf("NewLocalPhotoStore: %v", err)
	}
	meta := domain.PutMeta{ContentHash: "deadbeef", SizeBytes: 3, ContentType: "image/webp"}
	if _, err := s.Put(context.Background(), storagedomain.NewItemID(), meta, bytes.NewReader([]byte("abc"))); !errors.Is(err, domain.ErrUnsupportedMediaType) {
		t.Fatalf("Put(unsupported content type) = %v, want ErrUnsupportedMediaType", err)
	}
}

func TestLocalPhotoStore_SupportsDirectURLFalse(t *testing.T) {
	s, err := adapter.NewLocalPhotoStore(t.TempDir(), 10<<20)
	if err != nil {
		t.Fatalf("NewLocalPhotoStore: %v", err)
	}
	if s.SupportsDirectURL() {
		t.Error("LocalPhotoStore.SupportsDirectURL() = true, want false")
	}
}

func TestLocalPhotoStore_URL(t *testing.T) {
	s, err := adapter.NewLocalPhotoStore(t.TempDir(), 10<<20)
	if err != nil {
		t.Fatalf("NewLocalPhotoStore: %v", err)
	}
	data := jpegBytes(t)
	meta := computeMeta(data, domain.ContentTypeJPEG)
	ref, err := s.Put(context.Background(), storagedomain.NewItemID(), meta, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.URL(context.Background(), ref, 5*time.Minute)
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	if got == "" {
		t.Fatal("URL returned an empty locator for a stored ref")
	}

	if _, err := s.URL(context.Background(), domain.StorageRef("items/nope/deadbeef.jpg"), 5*time.Minute); !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Fatalf("URL(unknown) error = %v, want ErrPhotoNotFound", err)
	}

	// A directory ref (e.g. the item prefix itself) is not a stored photo.
	// path.Dir (not filepath.Dir): ref is always forward-slash-shaped
	// (buildStorageKey), regardless of OS.
	dirRef := domain.StorageRef(path.Dir(ref.String()))
	if _, err := s.URL(context.Background(), dirRef, 5*time.Minute); !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Fatalf("URL(directory) error = %v, want ErrPhotoNotFound", err)
	}
}
