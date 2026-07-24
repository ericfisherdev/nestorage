package config_test

import (
	"strings"
	"testing"

	corecfg "github.com/ericfisherdev/nestcore/config"

	"github.com/ericfisherdev/nestorage/internal/platform/config"
)

// baseMediaEnv is the minimal environment Load needs to succeed, reused by
// every case below so each test only sets the MEDIA_* value it cares about.
func baseMediaEnv() map[string]string {
	return map[string]string{
		"APP_ENV":      corecfg.EnvDev,
		"DATABASE_URL": "postgres://u:p@example.com:5432/nestorage?sslmode=disable",
	}
}

func TestLoad_MediaDefaults(t *testing.T) {
	setEnv(t, baseMediaEnv())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Media.Root != "./media" {
		t.Errorf("Media.Root = %q, want the default %q", cfg.Media.Root, "./media")
	}
	if cfg.Media.MaxUploadBytes != 10<<20 {
		t.Errorf("Media.MaxUploadBytes = %d, want the default %d", cfg.Media.MaxUploadBytes, 10<<20)
	}
}

func TestLoad_MediaOverrides(t *testing.T) {
	env := baseMediaEnv()
	env["MEDIA_ROOT"] = "/var/lib/nestorage/media"
	env["MEDIA_MAX_UPLOAD_BYTES"] = "26214400"
	setEnv(t, env)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Media.Root != "/var/lib/nestorage/media" {
		t.Errorf("Media.Root = %q, want the overridden value", cfg.Media.Root)
	}
	if cfg.Media.MaxUploadBytes != 26214400 {
		t.Errorf("Media.MaxUploadBytes = %d, want 26214400", cfg.Media.MaxUploadBytes)
	}
}

// TestLoad_MediaThumbMaxEdgeDefault covers NSTR-84's AC that a fresh install
// needs zero MEDIA_THUMB_MAX_EDGE configuration.
func TestLoad_MediaThumbMaxEdgeDefault(t *testing.T) {
	setEnv(t, baseMediaEnv())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Media.ThumbMaxEdge != 400 {
		t.Errorf("Media.ThumbMaxEdge = %d, want the default %d", cfg.Media.ThumbMaxEdge, 400)
	}
}

func TestLoad_MediaThumbMaxEdgeOverride(t *testing.T) {
	env := baseMediaEnv()
	env["MEDIA_THUMB_MAX_EDGE"] = "800"
	setEnv(t, env)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Media.ThumbMaxEdge != 800 {
		t.Errorf("Media.ThumbMaxEdge = %d, want 800", cfg.Media.ThumbMaxEdge)
	}
}

func TestLoad_MediaThumbMaxEdgeNonPositiveRejected(t *testing.T) {
	env := baseMediaEnv()
	env["MEDIA_THUMB_MAX_EDGE"] = "0"
	setEnv(t, env)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "MEDIA_THUMB_MAX_EDGE") {
		t.Fatalf("Load() error = %v, want an error naming MEDIA_THUMB_MAX_EDGE", err)
	}
}

func TestLoad_MediaThumbMaxEdgeUnparsableRejected(t *testing.T) {
	env := baseMediaEnv()
	env["MEDIA_THUMB_MAX_EDGE"] = "not-a-number"
	setEnv(t, env)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "MEDIA_THUMB_MAX_EDGE") {
		t.Fatalf("Load() error = %v, want an error naming MEDIA_THUMB_MAX_EDGE", err)
	}
}

func TestLoad_MediaRootBlankRejected(t *testing.T) {
	env := baseMediaEnv()
	env["MEDIA_ROOT"] = "   "
	setEnv(t, env)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "MEDIA_ROOT") {
		t.Fatalf("Load() error = %v, want an error naming MEDIA_ROOT", err)
	}
}

func TestLoad_MediaMaxUploadBytesNonPositiveRejected(t *testing.T) {
	env := baseMediaEnv()
	env["MEDIA_MAX_UPLOAD_BYTES"] = "0"
	setEnv(t, env)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "MEDIA_MAX_UPLOAD_BYTES") {
		t.Fatalf("Load() error = %v, want an error naming MEDIA_MAX_UPLOAD_BYTES", err)
	}
}

func TestLoad_MediaMaxUploadBytesUnparsableRejected(t *testing.T) {
	env := baseMediaEnv()
	env["MEDIA_MAX_UPLOAD_BYTES"] = "not-a-number"
	setEnv(t, env)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "MEDIA_MAX_UPLOAD_BYTES") {
		t.Fatalf("Load() error = %v, want an error naming MEDIA_MAX_UPLOAD_BYTES", err)
	}
}

// TestLoad_MediaStorageBackendDefaultsLocal covers NSTR-35's AC that a fresh
// install needs zero MEDIA_STORAGE_BACKEND/S3_* configuration.
func TestLoad_MediaStorageBackendDefaultsLocal(t *testing.T) {
	setEnv(t, baseMediaEnv())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Media.Backend != config.MediaStorageBackendLocal {
		t.Errorf("Media.Backend = %q, want the default %q", cfg.Media.Backend, config.MediaStorageBackendLocal)
	}
}

func TestLoad_MediaStorageBackendUnknownRejected(t *testing.T) {
	env := baseMediaEnv()
	env["MEDIA_STORAGE_BACKEND"] = "azure-blob"
	setEnv(t, env)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "MEDIA_STORAGE_BACKEND") {
		t.Fatalf("Load() error = %v, want an error naming MEDIA_STORAGE_BACKEND", err)
	}
}

// TestLoad_MediaStorageBackendS3RequiresS3Vars covers the caller-gating
// contract (LoadMedia/Validate's own docs): selecting the s3 backend without
// S3_BUCKET/S3_REGION must fail Load naming them.
func TestLoad_MediaStorageBackendS3RequiresS3Vars(t *testing.T) {
	env := baseMediaEnv()
	env["MEDIA_STORAGE_BACKEND"] = "s3"
	setEnv(t, env)

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want an error naming the missing S3_* vars")
	}
	if !strings.Contains(err.Error(), "S3_BUCKET") {
		t.Errorf("Load() error = %v, want it to name S3_BUCKET", err)
	}
	if !strings.Contains(err.Error(), "S3_REGION") {
		t.Errorf("Load() error = %v, want it to name S3_REGION", err)
	}
}

// TestLoad_MediaStorageBackendLocalIgnoresPartialS3Vars covers the reverse
// of the gate above: a stray/partial S3_* setting left over from a
// copy-pasted .env must NOT fail a local-backend Load (LoadMedia's own doc).
func TestLoad_MediaStorageBackendLocalIgnoresPartialS3Vars(t *testing.T) {
	env := baseMediaEnv()
	env["S3_PRESIGN_TTL"] = "not-a-duration"
	setEnv(t, env)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil (S3_* is ignored on the local backend)", err)
	}
	if cfg.Media.Backend != config.MediaStorageBackendLocal {
		t.Errorf("Media.Backend = %q, want %q", cfg.Media.Backend, config.MediaStorageBackendLocal)
	}
}

// TestLoad_MediaStorageBackendS3Valid covers a fully-configured s3 backend
// loading cleanly, with every S3 field wired through to cfg.Media.S3.
func TestLoad_MediaStorageBackendS3Valid(t *testing.T) {
	env := baseMediaEnv()
	env["MEDIA_STORAGE_BACKEND"] = "s3"
	env["S3_ENDPOINT"] = "http://127.0.0.1:59001"
	env["S3_REGION"] = "us-east-1"
	env["S3_BUCKET"] = "nestorage-photos"
	env["S3_ACCESS_KEY_ID"] = "test"
	env["S3_SECRET_ACCESS_KEY"] = "testtest"
	env["S3_USE_PATH_STYLE"] = "true"
	setEnv(t, env)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Media.Backend != config.MediaStorageBackendS3 {
		t.Errorf("Media.Backend = %q, want %q", cfg.Media.Backend, config.MediaStorageBackendS3)
	}
	if cfg.Media.S3.Bucket != "nestorage-photos" {
		t.Errorf("Media.S3.Bucket = %q, want %q", cfg.Media.S3.Bucket, "nestorage-photos")
	}
	if cfg.Media.S3.Endpoint != "http://127.0.0.1:59001" {
		t.Errorf("Media.S3.Endpoint = %q, want %q", cfg.Media.S3.Endpoint, "http://127.0.0.1:59001")
	}
	if !cfg.Media.S3.UsePathStyle {
		t.Error("Media.S3.UsePathStyle = false, want true")
	}
}
