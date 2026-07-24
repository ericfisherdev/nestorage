// Package bootstrap builds the media bounded context's composition-root-only
// dependency: NewPhotoStore, the domain.PhotoStore selector cmd/server calls
// to construct whichever backend config.MediaConfig.Backend names. It is
// deliberately its own package, not folded into internal/media/adapter:
// that package's own doc (see S3Params) states it never depends on
// internal/platform/config, so a config-consuming builder belongs in a peer
// package instead — mirroring Nestova's identical NES-132 rationale for its
// own internal/media/bootstrap.
package bootstrap

import (
	"context"
	"fmt"

	mediaadapter "github.com/ericfisherdev/nestorage/internal/media/adapter"
	mediadomain "github.com/ericfisherdev/nestorage/internal/media/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/config"
)

// NewPhotoStore constructs the domain.PhotoStore config.MediaConfig.Backend
// selects: the local filesystem adapter for MediaStorageBackendLocal (the
// default, and every value this ticket does not explicitly special-case —
// config.MediaConfig.Validate already rejects anything else at startup, so
// this fallback is never reached with an unrecognized backend in practice),
// or the S3-compatible adapter for MediaStorageBackendS3 (NSTR-35).
//
// Unlike Nestova's NES-132 resolver (which keeps BOTH backends constructed
// side by side, so historical rows written under a since-abandoned backend
// stay readable), Nestorage's photo table has no production history to
// preserve yet — PhotoService holds exactly one active domain.PhotoStore
// (see its own doc), and switching MEDIA_STORAGE_BACKEND is an
// operator-driven, pre-migration decision. The storage_backend column this
// ticket's reconciliation confirms on the photo table exists so a later
// ticket can add the read-routing-by-row's-own-backend NES-132 style
// resolver would provide, without a schema change at that point.
//
// ctx bounds the S3 backend's startup HeadBucket reachability check (see
// mediaadapter.NewS3PhotoStore's doc) — callers are expected to derive it
// with a bounded timeout so a network-unreachable S3 endpoint fails boot
// promptly instead of hanging indefinitely; the local backend's
// construction has no meaningful cancellation point and ignores ctx
// entirely.
func NewPhotoStore(ctx context.Context, mediaCfg config.MediaConfig) (mediadomain.PhotoStore, error) {
	if mediaCfg.Backend == config.MediaStorageBackendS3 {
		store, err := mediaadapter.NewS3PhotoStore(ctx, mediaadapter.S3Params{
			Endpoint:        mediaCfg.S3.Endpoint,
			Region:          mediaCfg.S3.Region,
			Bucket:          mediaCfg.S3.Bucket,
			AccessKeyID:     mediaCfg.S3.AccessKeyID,
			SecretAccessKey: mediaCfg.S3.SecretAccessKey,
			UsePathStyle:    mediaCfg.S3.UsePathStyle,
			PresignTTL:      mediaCfg.S3.PresignTTL,
		})
		if err != nil {
			return nil, fmt.Errorf("media/bootstrap: create s3 photo store: %w", err)
		}
		return store, nil
	}

	store, err := mediaadapter.NewLocalPhotoStore(mediaCfg.Root, mediaCfg.MaxUploadBytes)
	if err != nil {
		return nil, fmt.Errorf("media/bootstrap: create local photo store: %w", err)
	}
	return store, nil
}
