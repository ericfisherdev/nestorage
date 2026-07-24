// Command backfill-thumbs generates and stores the thumbnail variant for
// every photo row created before NSTR-84 shipped (thumb_storage_ref IS
// NULL). It is deliberately a standalone one-shot command, not a migration
// and not a server route: generating thumbnails for an existing photo
// library is I/O- and CPU-bound work that must never run inside a migration
// transaction or block server startup.
//
// Safe to re-run: a photo whose thumbnail reference is already set is never
// selected again (PhotoRepository.ListMissingThumbnail), so a systemd timer
// or a manual re-run after a partial failure both converge on the same
// result without redoing completed work.
//
// Usage:
//
//	go run ./cmd/backfill-thumbs
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ericfisherdev/nestcore/db"

	mediaadapter "github.com/ericfisherdev/nestorage/internal/media/adapter"
	mediabootstrap "github.com/ericfisherdev/nestorage/internal/media/bootstrap"
	mediadomain "github.com/ericfisherdev/nestorage/internal/media/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/config"
)

// batchSize bounds how many rows ListMissingThumbnail returns per page —
// small enough that a decoded image never dominates the process's memory,
// large enough that a large library does not need thousands of round trips.
const batchSize = 50

// mediaBootstrapTimeout bounds mediabootstrap.NewPhotoStore's S3 backend
// reachability check, mirroring cmd/server's identical constant/rationale.
const mediaBootstrapTimeout = 15 * time.Second

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("backfill-thumbs exited with error", "error", err)
		os.Exit(1)
	}
}

// run loads configuration, resolves the same PhotoStore backend the server
// uses, and backfills every photo whose thumbnail reference is still NULL.
// Returns an error (nonzero exit) when configuration/connectivity fails to
// establish, or when any individual photo failed to backfill — a systemd
// unit or operator sees that rather than a silent partial success.
func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	pool, err := db.New(ctx, cfg.DB)
	if err != nil {
		return err
	}
	defer pool.Close()

	mediaCtx, cancelMedia := context.WithTimeout(ctx, mediaBootstrapTimeout)
	store, err := mediabootstrap.NewPhotoStore(mediaCtx, cfg.Media)
	cancelMedia()
	if err != nil {
		return err
	}

	thumbnailer, err := mediaadapter.NewImageThumbnailer(cfg.Media.ThumbMaxEdge)
	if err != nil {
		return err
	}

	repo := mediaadapter.NewPhotoRepository(pool)

	failed, err := backfill(ctx, repo, store, thumbnailer, logger)
	if err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("backfill-thumbs: %d photo(s) failed to backfill (see the logged errors above)", failed)
	}
	return nil
}

// thumbnailRepo is the narrow port (ISP) backfill depends on, satisfied by
// *mediaadapter.PhotoRepository (a superset) and by test fakes.
type thumbnailRepo interface {
	ListMissingThumbnail(ctx context.Context, limit int, afterID mediadomain.PhotoID) ([]mediadomain.MissingThumbnailPhoto, error)
	SetThumbnailRef(ctx context.Context, photoID mediadomain.PhotoID, ref mediadomain.StorageRef) error
}

// backfill pages through every photo whose thumbnail reference is still
// NULL (repo.ListMissingThumbnail's resumable id cursor), generating and
// storing a thumbnail for each. A per-photo failure is logged and does NOT
// abort the run — every other photo still gets a chance — and the cursor
// still advances past the failed row so this run does not spin on it
// forever; a later run retries it, since a failed row never got
// SetThumbnailRef called. Returns the number of photos that failed.
func backfill(ctx context.Context, repo thumbnailRepo, store mediadomain.PhotoStore, thumbnailer mediadomain.PhotoThumbnailer, logger *slog.Logger) (int, error) {
	var (
		afterID mediadomain.PhotoID
		failed  int
	)
	for {
		rows, err := repo.ListMissingThumbnail(ctx, batchSize, afterID)
		if err != nil {
			return failed, fmt.Errorf("backfill-thumbs: list photos missing a thumbnail: %w", err)
		}
		if len(rows) == 0 {
			return failed, nil
		}

		for _, row := range rows {
			if err := backfillOne(ctx, repo, store, thumbnailer, row); err != nil {
				failed++
				logger.ErrorContext(ctx, "backfill-thumbs: photo failed", "photo_id", row.ID.String(), "error", err)
			}
			afterID = row.ID
		}
	}
}

// backfillOne generates and stores photoRow's thumbnail: opens its full
// object, thumbnails the bytes, Puts the thumb under its own key (keyed by
// the SAME content hash as the full image, matching
// media/app.PhotoService.Upload's own convention), and records the
// resulting reference.
func backfillOne(ctx context.Context, repo thumbnailRepo, store mediadomain.PhotoStore, thumbnailer mediadomain.PhotoThumbnailer, row mediadomain.MissingThumbnailPhoto) error {
	rc, err := store.Open(ctx, row.StorageRef)
	if err != nil {
		return fmt.Errorf("open full photo: %w", err)
	}
	data, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	if readErr != nil {
		return fmt.Errorf("read full photo: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close full photo: %w", closeErr)
	}

	thumb, err := thumbnailer.Thumbnail(data, row.ContentType)
	if err != nil {
		return fmt.Errorf("generate thumbnail: %w", err)
	}

	meta := mediadomain.PutMeta{
		ContentHash: row.ContentHash,
		SizeBytes:   int64(len(thumb.Bytes)),
		ContentType: thumb.ContentType,
		Variant:     mediadomain.PhotoVariantThumb,
	}
	ref, err := store.Put(ctx, row.ItemID, meta, bytes.NewReader(thumb.Bytes))
	if err != nil {
		return fmt.Errorf("store thumbnail: %w", err)
	}

	if err := repo.SetThumbnailRef(ctx, row.ID, ref); err != nil {
		return fmt.Errorf("record thumbnail reference: %w", err)
	}
	return nil
}
