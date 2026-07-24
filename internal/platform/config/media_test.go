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
