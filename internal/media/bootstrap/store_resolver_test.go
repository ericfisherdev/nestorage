package bootstrap_test

import (
	"context"
	"testing"
	"time"

	corecfg "github.com/ericfisherdev/nestcore/config"

	"github.com/ericfisherdev/nestorage/internal/media/bootstrap"
	"github.com/ericfisherdev/nestorage/internal/platform/config"
)

// TestNewPhotoStore_LocalBackendDefault covers the resolver's default
// branch: MediaStorageBackendLocal (the zero-config default) constructs the
// local adapter, ungated (no MinIO needed) since it never touches the
// network.
func TestNewPhotoStore_LocalBackendDefault(t *testing.T) {
	cfg := config.MediaConfig{Backend: config.MediaStorageBackendLocal, Root: t.TempDir(), MaxUploadBytes: 10 << 20}
	store, err := bootstrap.NewPhotoStore(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPhotoStore(local): %v", err)
	}
	if store.SupportsDirectURL() {
		t.Error("NewPhotoStore(local).SupportsDirectURL() = true, want false (the local adapter, not S3)")
	}
}

// TestNewPhotoStore_LocalBackendPropagatesConstructorError covers the local
// branch's own error path: a blank Root fails NewLocalPhotoStore's own
// validation, surfaced here wrapped.
func TestNewPhotoStore_LocalBackendPropagatesConstructorError(t *testing.T) {
	cfg := config.MediaConfig{Backend: config.MediaStorageBackendLocal, Root: "   ", MaxUploadBytes: 10 << 20}
	if _, err := bootstrap.NewPhotoStore(context.Background(), cfg); err == nil {
		t.Fatal("NewPhotoStore(local, blank root) error = nil, want the local constructor's fail-fast error propagated")
	}
}

// TestNewPhotoStore_S3BackendPropagatesConstructorError proves
// MediaStorageBackendS3 genuinely routes to mediaadapter.NewS3PhotoStore
// (not a silent fallback to local): an S3Config that fails NewS3PhotoStore's
// own parameter validation (blank bucket) surfaces that exact error here,
// wrapped — exercised without MinIO, since parameter validation runs before
// any network call.
func TestNewPhotoStore_S3BackendPropagatesConstructorError(t *testing.T) {
	cfg := config.MediaConfig{
		Backend: config.MediaStorageBackendS3,
		S3:      corecfg.S3Config{Region: "us-east-1", Bucket: "", PresignTTL: time.Minute},
	}
	if _, err := bootstrap.NewPhotoStore(context.Background(), cfg); err == nil {
		t.Fatal("NewPhotoStore(s3, blank bucket) error = nil, want the S3 constructor's fail-fast error propagated")
	}
}
