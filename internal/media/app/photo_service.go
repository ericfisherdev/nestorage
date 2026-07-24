package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/media/domain"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// itemGetter is the narrow ISP port PhotoService depends on to resolve an
// item's visibility — satisfied by storagedomain.ItemRepository (a
// superset) and by test fakes, the same narrowing pattern
// storage/app.itemRepository already establishes. Sprint 5 reconciliation
// R5: PhotoService took no viewer in the original plan, which meant any
// authenticated caller could read any item's photos by id; every exported
// PhotoService method below resolves itemID through this port FIRST and
// propagates storagedomain.ErrItemNotFound when the viewer may not see it,
// before ever touching a photo.
type itemGetter interface {
	Get(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID) (*storagedomain.Item, error)
}

// PhotoService orchestrates the upload/read/delete pipeline the Sprint 5
// reconciliation (R1) binds: stage+validate (PhotoValidator) -> hash the
// validated bytes -> Put (PhotoStore, keyed by the item and that hash) ->
// dedup-probe (FindByStorageRef) -> persist (PhotoRepository.Create) ->
// attach (AttachToItem). NSTR-36 inserts a scrub step between validate and
// hash by adding its own PhotoScrubber dependency here; this ticket's
// pipeline computes the hash directly over the validated bytes, so a photo
// uploaded before NSTR-36 lands reads back byte-identical, full stop (the
// AC's "apart from EXIF removal" clause becomes observable only once that
// ticket adds the scrub step).
//
// Every method below is visibility-scoped to viewer via itemGetter (Sprint
// 5 reconciliation R5) — see itemGetter's own doc.
type PhotoService struct {
	store          domain.PhotoStore
	validator      domain.PhotoValidator
	photos         domain.PhotoRepository
	items          itemGetter
	maxUploadBytes int64
	logger         *slog.Logger
}

// NewPhotoService constructs PhotoService with injected dependencies. A nil
// dependency panics (matching every other constructor in this codebase,
// e.g. storage/app.NewItemService) — a programming error, not a runtime
// condition a caller could recover from. maxUploadBytes instead returns an
// error when it is not positive, mirroring
// media/adapter.NewLocalPhotoStore's identical config-value check: it is a
// value that flows from parsed configuration, not a structural dependency,
// so the composition root can fail startup gracefully rather than panic.
func NewPhotoService(store domain.PhotoStore, validator domain.PhotoValidator, photos domain.PhotoRepository, items itemGetter, maxUploadBytes int64, logger *slog.Logger) (*PhotoService, error) {
	switch {
	case store == nil:
		panic("media/app: NewPhotoService requires a non-nil PhotoStore")
	case validator == nil:
		panic("media/app: NewPhotoService requires a non-nil PhotoValidator")
	case photos == nil:
		panic("media/app: NewPhotoService requires a non-nil PhotoRepository")
	case items == nil:
		panic("media/app: NewPhotoService requires a non-nil itemGetter")
	case logger == nil:
		panic("media/app: NewPhotoService requires a non-nil logger")
	}
	if maxUploadBytes <= 0 {
		return nil, fmt.Errorf("media/app: max upload bytes must be positive, got %d", maxUploadBytes)
	}
	return &PhotoService{
		store: store, validator: validator, photos: photos, items: items,
		maxUploadBytes: maxUploadBytes, logger: logger,
	}, nil
}

// Upload validates and stores r as a new photo attached to itemID,
// attributed to viewer, first confirming itemID is visible to viewer
// (storagedomain.ErrItemNotFound otherwise). It streams r to a staging file
// (PhotoValidator.ValidateAndStage), hashes the validated bytes, hands them
// to PhotoStore.Put keyed by itemID and that hash, and persists the
// resulting Photo, attached at the next position (primary if it is the
// item's first photo).
//
// Put's key is content-addressed and item-scoped: re-uploading the same
// bytes to the same item lands on the same key, so if a photo with that
// StorageRef already exists (FindByStorageRef, or a concurrent upload
// losing the race and surfacing ErrDuplicatePhoto from Create) Upload
// returns the EXISTING photo rather than creating a second row.
func (s *PhotoService) Upload(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, r io.Reader) (*domain.Photo, error) {
	if _, err := s.items.Get(ctx, viewer, itemID); err != nil {
		return nil, err
	}

	staged, err := s.validator.ValidateAndStage(ctx, r, s.maxUploadBytes)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(staged.Path) }()

	hash, err := hashFile(staged.Path)
	if err != nil {
		return nil, fmt.Errorf("media/app: hash staged upload: %w", err)
	}

	f, err := os.Open(staged.Path)
	if err != nil {
		return nil, fmt.Errorf("media/app: open staged upload: %w", err)
	}
	defer func() { _ = f.Close() }()

	meta := domain.PutMeta{ContentHash: hash, SizeBytes: staged.SizeBytes, ContentType: staged.ContentType}
	ref, err := s.store.Put(ctx, itemID, meta, f)
	if err != nil {
		return nil, err
	}

	if existing, findErr := s.photos.FindByStorageRef(ctx, ref); findErr == nil {
		return existing, nil
	} else if !errors.Is(findErr, domain.ErrPhotoNotFound) {
		return nil, fmt.Errorf("media/app: check duplicate photo: %w", findErr)
	}

	photo := &domain.Photo{
		ID:             domain.NewPhotoID(),
		StorageRef:     ref,
		ContentHash:    hash,
		SizeBytes:      staged.SizeBytes,
		ContentType:    staged.ContentType,
		StorageBackend: domain.StorageBackendLocal,
		UploadedBy:     viewer.UserID,
	}
	if err := photo.Validate(); err != nil {
		return nil, err
	}
	if err := s.photos.Create(ctx, photo); err != nil {
		if errors.Is(err, domain.ErrDuplicatePhoto) {
			// Lost a race with a concurrent upload of the same bytes to the
			// same item: fetch and return the winner's row instead of
			// surfacing an error.
			existing, findErr := s.photos.FindByStorageRef(ctx, ref)
			if findErr != nil {
				return nil, fmt.Errorf("media/app: resolve concurrent duplicate: %w", findErr)
			}
			return existing, nil
		}
		return nil, err
	}

	existing, err := s.photos.ListByItem(ctx, itemID)
	if err != nil {
		return nil, fmt.Errorf("media/app: list existing item photos: %w", err)
	}
	position := len(existing)
	isPrimary := position == 0
	if err := s.photos.AttachToItem(ctx, itemID, photo.ID, position, isPrimary); err != nil {
		return nil, err
	}

	s.logAction(ctx, "photo uploaded", itemID, photo.ID)
	return photo, nil
}

// ListForItem returns itemID's attached photos ordered by position, first
// confirming itemID is visible to viewer.
func (s *PhotoService) ListForItem(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID) ([]domain.ItemPhoto, error) {
	if _, err := s.items.Get(ctx, viewer, itemID); err != nil {
		return nil, err
	}
	photos, err := s.photos.ListByItem(ctx, itemID)
	if err != nil {
		return nil, fmt.Errorf("media/app: list item photos: %w", err)
	}
	return photos, nil
}

// Open streams photoID's stored bytes back, first confirming itemID is
// visible to viewer and that photoID is attached to itemID (GetForItem —
// see its own doc for why a bare photo id is never trusted alone). Returns
// the photo's content type alongside the stream so the caller can set an
// explicit Content-Type rather than letting the client sniff it.
func (s *PhotoService) Open(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, photoID domain.PhotoID) (io.ReadCloser, string, error) {
	photo, err := s.viewablePhoto(ctx, viewer, itemID, photoID)
	if err != nil {
		return nil, "", err
	}
	rc, err := s.store.Open(ctx, photo.StorageRef)
	if err != nil {
		return nil, "", err
	}
	return rc, photo.ContentType, nil
}

// DirectURL returns a locator for photoID's stored bytes (see
// PhotoStore.URL), first confirming itemID is visible to viewer and that
// photoID is attached to itemID. Callers should check SupportsDirectURL
// before deciding whether to redirect a client to the result or to stream
// it through Open instead.
func (s *PhotoService) DirectURL(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, photoID domain.PhotoID) (string, error) {
	photo, err := s.viewablePhoto(ctx, viewer, itemID, photoID)
	if err != nil {
		return "", err
	}
	url, err := s.store.URL(ctx, photo.StorageRef, 0)
	if err != nil {
		return "", err
	}
	return url, nil
}

// SupportsDirectURL reports whether DirectURL returns a browser-navigable
// locator (see PhotoStore.SupportsDirectURL's own doc). It carries no
// per-item data, so — unlike every other method here — it is not
// visibility-scoped: there is nothing item-specific to leak.
func (s *PhotoService) SupportsDirectURL() bool { return s.store.SupportsDirectURL() }

// Delete removes photoID (first confirming itemID is visible to viewer and
// that photoID is attached to itemID), then its stored bytes: the
// PhotoRepository row(s) first, the PhotoStore object last (see
// domain.PhotoRepository's own doc for why item-scoped dedup makes this
// ordering safe with no refcounting).
func (s *PhotoService) Delete(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, photoID domain.PhotoID) error {
	photo, err := s.viewablePhoto(ctx, viewer, itemID, photoID)
	if err != nil {
		return err
	}
	if err := s.photos.Delete(ctx, itemID, photoID); err != nil {
		return err
	}
	if err := s.store.Delete(ctx, photo.StorageRef); err != nil {
		return fmt.Errorf("media/app: delete stored photo bytes: %w", err)
	}
	s.logAction(ctx, "photo deleted", itemID, photoID)
	return nil
}

// SetPrimary marks photoID as itemID's primary photo, first confirming
// itemID is visible to viewer and that photoID is attached to itemID.
func (s *PhotoService) SetPrimary(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, photoID domain.PhotoID) error {
	if _, err := s.viewablePhoto(ctx, viewer, itemID, photoID); err != nil {
		return err
	}
	if err := s.photos.SetPrimary(ctx, itemID, photoID); err != nil {
		return err
	}
	s.logAction(ctx, "photo set primary", itemID, photoID)
	return nil
}

// Reorder rewrites itemID's attached photos' positions to match order,
// first confirming itemID is visible to viewer. order is expected to list
// every photo currently attached to itemID exactly once (see
// domain.PhotoRepository.Reorder's own doc) — callers build it from a
// preceding ListForItem.
func (s *PhotoService) Reorder(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, order []domain.PhotoID) error {
	if _, err := s.items.Get(ctx, viewer, itemID); err != nil {
		return err
	}
	if err := s.photos.Reorder(ctx, itemID, order); err != nil {
		return err
	}
	s.logAction(ctx, "photos reordered", itemID, domain.PhotoID{})
	return nil
}

// viewablePhoto confirms itemID is visible to viewer and that photoID is
// attached to itemID, shared by every method keyed on one existing photo
// (Open, DirectURL, Delete, SetPrimary) so the two-step visibility check
// (Sprint 5 reconciliation R5) is written exactly once.
func (s *PhotoService) viewablePhoto(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, photoID domain.PhotoID) (*domain.Photo, error) {
	if _, err := s.items.Get(ctx, viewer, itemID); err != nil {
		return nil, err
	}
	photo, err := s.photos.GetForItem(ctx, itemID, photoID)
	if err != nil {
		return nil, err
	}
	return photo, nil
}

// hashFile returns the hex sha256 of the file at path — Upload's "hash the
// validated bytes" step (Sprint 5 reconciliation R1), run once the staged
// file has already passed PhotoValidator.ValidateAndStage.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open staged file: %w", err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("read staged file: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// logAction writes one INFO-level audit line for a completed photo
// mutation. It logs the item and photo ids, never any filename or content —
// Nestorage's PII-out-of-logs convention (see identity/app.AdminService.logAction).
// A zero PhotoID (Reorder, which has no single photo to name) is logged as
// an empty string rather than the zero UUID's misleading-looking value.
func (s *PhotoService) logAction(ctx context.Context, msg string, itemID storagedomain.ItemID, photoID domain.PhotoID) {
	args := []any{"item_id", itemID.String()}
	if zero := (domain.PhotoID{}); photoID != zero {
		args = append(args, "photo_id", photoID.String())
	}
	s.logger.InfoContext(ctx, "media: "+msg, args...)
}
