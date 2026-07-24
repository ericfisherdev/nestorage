package config

// media.go adds photo-storage configuration: where the local PhotoStore
// writes photo bytes and the per-upload size cap (NSTR-34), plus which
// PhotoStore backend is active and its S3 settings (NSTR-35). nestcore has
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

// defaultThumbMaxEdge is MEDIA_THUMB_MAX_EDGE's default (NSTR-84): the
// generated thumbnail variant's long edge, in pixels.
const defaultThumbMaxEdge int32 = 400

// MediaStorageBackend selects which domain.PhotoStore implementation
// bootstrap.NewPhotoStore constructs (NSTR-35). It is a plain string type
// (not an iota) so its .env/log spelling is its own zero-translation
// representation, mirroring media/domain.StorageBackend's identical
// rationale — the two are deliberately kept as separate types (this one
// selects a backend at boot, that one records which backend already wrote a
// given row) even though their values agree.
type MediaStorageBackend string

// MediaStorageBackend values. Local (LocalPhotoStore, NSTR-34's default) is
// always available with no configuration; S3 (S3PhotoStore, NSTR-35) is
// opt-in and requires MediaConfig.S3 to be populated.
const (
	MediaStorageBackendLocal MediaStorageBackend = "local"
	MediaStorageBackendS3    MediaStorageBackend = "s3"
)

// MediaConfig configures photo storage: where the local PhotoStore writes
// photo bytes, the per-upload size cap enforced by media/app.PhotoService
// before any bytes reach a PhotoStore, which backend is active
// (MEDIA_STORAGE_BACKEND), and — only when that backend is s3 — the S3
// settings bootstrap.NewPhotoStore builds an S3PhotoStore from.
type MediaConfig struct {
	// Root is the directory the local PhotoStore writes photo bytes under.
	// Consulted regardless of Backend: bootstrap.NewPhotoStore's local
	// adapter is always constructible (NSTR-35's mixed-state requirement —
	// see media/domain.Photo's own doc).
	Root string
	// MaxUploadBytes caps a single photo upload (bytes).
	MaxUploadBytes int64
	// ThumbMaxEdge bounds the generated thumbnail variant's long edge, in
	// pixels (NSTR-84) — passed straight to
	// media/adapter.NewImageThumbnailer.
	ThumbMaxEdge int
	// Backend selects which PhotoStore implementation bootstrap.NewPhotoStore
	// returns. Defaults to MediaStorageBackendLocal.
	Backend MediaStorageBackend
	// S3 configures the S3-compatible backend; consulted (and validated)
	// only when Backend is MediaStorageBackendS3 — see Validate's own doc.
	S3 corecfg.S3Config
}

// LoadMedia reads MediaConfig from MEDIA_ROOT, MEDIA_MAX_UPLOAD_BYTES,
// MEDIA_STORAGE_BACKEND, and — caller-gated on the selected backend — the
// S3_* variables via corecfg.LoadS3, mirroring corecfg.LoadServer/LoadDB's
// (value, []error) shape so Load can aggregate its errors the same way.
//
// LoadS3 itself always parses every S3_* field regardless of Backend (see
// its own doc), but its returned errors are appended here ONLY when Backend
// is actually s3 — this exact composition pattern is proven in nestcore's
// config/config_test.go (composeAppConfig's s3Enabled gate) — so a
// local-only install is never forced to define S3_* vars, and a stray or
// partially-set S3_* left over from a copy-pasted .env never fails a
// local-backend Load.
func LoadMedia() (MediaConfig, []error) {
	var errs []error

	maxUploadBytes, err := corecfg.Int64("MEDIA_MAX_UPLOAD_BYTES", defaultMaxUploadBytes)
	if err != nil {
		errs = append(errs, err)
	}
	thumbMaxEdge, err := corecfg.Int32("MEDIA_THUMB_MAX_EDGE", defaultThumbMaxEdge)
	if err != nil {
		errs = append(errs, err)
	}
	backend := MediaStorageBackend(strings.ToLower(strings.TrimSpace(
		corecfg.String("MEDIA_STORAGE_BACKEND", string(MediaStorageBackendLocal)),
	)))

	s3Cfg, s3Errs := corecfg.LoadS3()
	if backend == MediaStorageBackendS3 {
		errs = append(errs, s3Errs...)
	}

	return MediaConfig{
		Root:           strings.TrimSpace(corecfg.String("MEDIA_ROOT", devMediaRoot)),
		MaxUploadBytes: maxUploadBytes,
		ThumbMaxEdge:   int(thumbMaxEdge),
		Backend:        backend,
		S3:             s3Cfg,
	}, errs
}

// Validate returns every MediaConfig problem found, so callers can surface
// them together. S3Config.Validate's findings are appended only when Backend
// is actually MediaStorageBackendS3 (see LoadMedia's own doc for why this
// gating matters) — mirroring LoadMedia's identical S3-gating.
func (m MediaConfig) Validate() []error {
	var errs []error
	if strings.TrimSpace(m.Root) == "" {
		errs = append(errs, errors.New("MEDIA_ROOT must not be empty"))
	}
	if m.MaxUploadBytes <= 0 {
		errs = append(errs, fmt.Errorf("MEDIA_MAX_UPLOAD_BYTES must be positive, got %d", m.MaxUploadBytes))
	}
	if m.ThumbMaxEdge <= 0 {
		errs = append(errs, fmt.Errorf("MEDIA_THUMB_MAX_EDGE must be positive, got %d", m.ThumbMaxEdge))
	}
	switch m.Backend {
	case MediaStorageBackendLocal, MediaStorageBackendS3:
	default:
		errs = append(errs, fmt.Errorf("MEDIA_STORAGE_BACKEND must be one of %s|%s, got %q", MediaStorageBackendLocal, MediaStorageBackendS3, m.Backend))
	}
	if m.Backend == MediaStorageBackendS3 {
		errs = append(errs, m.S3.Validate()...)
	}
	return errs
}
