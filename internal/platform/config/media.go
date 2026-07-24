package config

// media.go adds photo-storage configuration (NSTR-34): where the local
// PhotoStore writes photo bytes, and the per-upload size cap. nestcore has
// no media loader (media is Nestorage's own bounded context, not a shared
// platform concern), so this is Nestorage-local, mirroring
// corecfg.CacheConfig's identical safe-default-in-every-environment shape.

import (
	"errors"
	"fmt"
	"strings"

	corecfg "github.com/ericfisherdev/nestcore/config"
)

// devMediaRoot is the default photo-storage directory when MEDIA_ROOT is
// unset — a fresh install needs zero configuration to start uploading.
const devMediaRoot = "./media"

// defaultMaxUploadBytes is MEDIA_MAX_UPLOAD_BYTES's default (10 MiB).
const defaultMaxUploadBytes int64 = 10 << 20

// MediaConfig configures photo storage: where the local PhotoStore writes
// photo bytes, and the per-upload size cap enforced by
// media/app.PhotoService before any bytes reach a PhotoStore.
type MediaConfig struct {
	// Root is the directory the local PhotoStore writes photo bytes under.
	Root string
	// MaxUploadBytes caps a single photo upload (bytes).
	MaxUploadBytes int64
}

// LoadMedia reads MediaConfig from MEDIA_ROOT and MEDIA_MAX_UPLOAD_BYTES,
// mirroring corecfg.LoadServer/LoadDB's (value, []error) shape so Load can
// aggregate its errors the same way.
func LoadMedia() (MediaConfig, []error) {
	var errs []error

	maxUploadBytes, err := corecfg.Int64("MEDIA_MAX_UPLOAD_BYTES", defaultMaxUploadBytes)
	if err != nil {
		errs = append(errs, err)
	}

	return MediaConfig{
		Root:           strings.TrimSpace(corecfg.String("MEDIA_ROOT", devMediaRoot)),
		MaxUploadBytes: maxUploadBytes,
	}, errs
}

// Validate returns every MediaConfig problem found, so callers can surface
// them together.
func (m MediaConfig) Validate() []error {
	var errs []error
	if strings.TrimSpace(m.Root) == "" {
		errs = append(errs, errors.New("MEDIA_ROOT must not be empty"))
	}
	if m.MaxUploadBytes <= 0 {
		errs = append(errs, fmt.Errorf("MEDIA_MAX_UPLOAD_BYTES must be positive, got %d", m.MaxUploadBytes))
	}
	return errs
}
