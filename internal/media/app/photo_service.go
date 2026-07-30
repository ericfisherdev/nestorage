package app

import (
	"bytes"
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
// reconciliation (R1) binds: stage+validate (PhotoValidator) -> scrub
// (PhotoScrubber, NSTR-36) -> hash the scrubbed bytes -> Put (PhotoStore,
// keyed by the item and that hash) -> dedup-probe (FindByStorageRef) ->
// persist (PhotoRepository.Create) -> attach (AttachToItem). The hash and
// the storage key are both derived from Scrub's OUTPUT, never the raw
// upload (Sprint 5 reconciliation R3), so a photo carrying EXIF GPS data can
// never reach PhotoStore.Put with that data intact, and re-uploading the
// same source photo twice still dedupes on the identical scrubbed bytes.
// NSTR-84 extends Upload with a thumbnail-generation step (PhotoThumbnailer)
// after that same dedup probe, and extends Open/DirectURL/Delete to handle
// the resulting thumbnail variant — see each method's own doc.
//
// Every method below is visibility-scoped to viewer via itemGetter (Sprint
// 5 reconciliation R5) — see itemGetter's own doc.
type PhotoService struct {
	store          domain.PhotoStore
	validator      domain.PhotoValidator
	scrubber       domain.PhotoScrubber
	thumbnailer    domain.PhotoThumbnailer
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
func NewPhotoService(store domain.PhotoStore, validator domain.PhotoValidator, scrubber domain.PhotoScrubber, thumbnailer domain.PhotoThumbnailer, photos domain.PhotoRepository, items itemGetter, maxUploadBytes int64, logger *slog.Logger) (*PhotoService, error) {
	switch {
	case store == nil:
		panic("media/app: NewPhotoService requires a non-nil PhotoStore")
	case validator == nil:
		panic("media/app: NewPhotoService requires a non-nil PhotoValidator")
	case scrubber == nil:
		panic("media/app: NewPhotoService requires a non-nil PhotoScrubber")
	case thumbnailer == nil:
		panic("media/app: NewPhotoService requires a non-nil PhotoThumbnailer")
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
		store: store, validator: validator, scrubber: scrubber, thumbnailer: thumbnailer, photos: photos, items: items,
		maxUploadBytes: maxUploadBytes, logger: logger,
	}, nil
}

// Upload validates and stores r as a new photo attached to itemID,
// attributed to viewer, first confirming itemID is visible to viewer
// (storagedomain.ErrItemNotFound otherwise). It streams r to a staging file
// (PhotoValidator.ValidateAndStage), scrubs the staged bytes
// (PhotoScrubber.Scrub — the whole image now lives in one buffer bounded by
// s.maxUploadBytes, which is expected: scrubbing needs the whole image, not
// a stream), hashes the SCRUBBED bytes, hands them to PhotoStore.Put keyed
// by itemID and that hash, and persists the resulting Photo, attached at the
// next position (primary if it is the item's first photo).
//
// Put's key is content-addressed and item-scoped: re-uploading the same
// source photo to the same item scrubs to the same bytes and lands on the
// same key, so if a photo with that StorageRef already exists
// (FindByStorageRef, or a concurrent upload losing the race and surfacing
// ErrDuplicatePhoto from Create) Upload returns the EXISTING photo rather
// than creating a second row (and never bothers generating a thumbnail for
// it — see the dedup check below).
//
// Once a genuinely new upload clears that dedup check, Upload generates a
// thumbnail from the SAME scrubbed bytes (PhotoThumbnailer, NSTR-84) and
// Puts it under its own key, keyed by the SAME content hash as the full
// image (see PutMeta.Variant/buildStorageKey) so item-scoped dedup keeps
// working for both variants. Thumbnail generation or storage failing does
// NOT fail the upload: the resulting Photo is created with a nil
// ThumbnailRef and the failure is logged at warn level — the household
// loses a size optimization, never a photo, and the serve path
// (Open/DirectURL) transparently falls back to the full image for a photo
// whose ThumbnailRef is nil.
func (s *PhotoService) Upload(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, r io.Reader) (*domain.Photo, error) {
	if _, err := s.items.Get(ctx, viewer, itemID); err != nil {
		return nil, err
	}

	staged, err := s.validator.ValidateAndStage(ctx, r, s.maxUploadBytes)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(staged.Path) }()

	raw, err := os.ReadFile(staged.Path)
	if err != nil {
		return nil, fmt.Errorf("media/app: read staged upload: %w", err)
	}

	scrubbed, err := s.scrubber.Scrub(ctx, raw, staged.ContentType)
	if err != nil {
		return nil, err
	}

	meta := domain.PutMeta{
		ContentHash: hashBytes(scrubbed),
		SizeBytes:   int64(len(scrubbed)),
		ContentType: staged.ContentType,
	}
	ref, err := s.store.Put(ctx, itemID, meta, bytes.NewReader(scrubbed))
	if err != nil {
		return nil, err
	}

	if existing, findErr := s.photos.FindByStorageRef(ctx, ref); findErr == nil {
		return existing, nil
	} else if !errors.Is(findErr, domain.ErrPhotoNotFound) {
		return nil, fmt.Errorf("media/app: check duplicate photo: %w", findErr)
	}

	thumbRef := s.tryGenerateThumbnail(ctx, itemID, scrubbed, meta.ContentType, meta.ContentHash)

	photo := &domain.Photo{
		ID:             domain.NewPhotoID(),
		HouseholdID:    viewer.HouseholdID,
		StorageRef:     ref,
		ThumbnailRef:   thumbRef,
		ContentHash:    meta.ContentHash,
		SizeBytes:      meta.SizeBytes,
		ContentType:    meta.ContentType,
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

// Open streams photoID's variant bytes back, first confirming itemID is
// visible to viewer and that photoID is attached to itemID (GetForItem —
// see its own doc for why a bare photo id is never trusted alone). Returns
// the photo's content type alongside the stream so the caller can set an
// explicit Content-Type rather than letting the client sniff it — this is
// always the FULL image's content type, which is also correct for the thumb
// variant, since PhotoThumbnailer always re-encodes at the same content
// type it decoded (see that port's own doc). variant selects which object
// resolveRef resolves to (see its own doc for the thumb-missing fallback).
func (s *PhotoService) Open(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, photoID domain.PhotoID, variant domain.PhotoVariant) (io.ReadCloser, string, error) {
	photo, err := s.viewablePhoto(ctx, viewer, itemID, photoID)
	if err != nil {
		return nil, "", err
	}
	rc, err := s.store.Open(ctx, s.resolveRef(photo, variant))
	if err != nil {
		return nil, "", err
	}
	return rc, photo.ContentType, nil
}

// DirectURL returns a locator for photoID's variant bytes (see
// PhotoStore.URL), first confirming itemID is visible to viewer and that
// photoID is attached to itemID. Callers should check SupportsDirectURL
// before deciding whether to redirect a client to the result or to stream
// it through Open instead. variant selects which object resolveRef resolves
// to (see its own doc for the thumb-missing fallback).
func (s *PhotoService) DirectURL(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, photoID domain.PhotoID, variant domain.PhotoVariant) (string, error) {
	photo, err := s.viewablePhoto(ctx, viewer, itemID, photoID)
	if err != nil {
		return "", err
	}
	url, err := s.store.URL(ctx, s.resolveRef(photo, variant), 0)
	if err != nil {
		return "", err
	}
	return url, nil
}

// SupportsDirectURL reports whether DirectURL returns a browser-navigable
// locator (see PhotoStore.SupportsDirectURL's own doc). It carries no
// per-item data, so — unlike every other method here — it is not
// visibility-scoped: there is nothing item-specific to leak. It is also
// deliberately variant-independent (mirrors PhotoStore.SupportsDirectURL's
// own doc): it describes the backend, not which object a given call
// resolves to, so a thumbnail request travels through exactly the same
// redirect-or-stream branch a full-image request does.
func (s *PhotoService) SupportsDirectURL() bool { return s.store.SupportsDirectURL() }

// resolveRef returns photo's StorageRef for variant: its ThumbnailRef when
// variant is PhotoVariantThumb AND one exists, its full StorageRef
// otherwise — both for a PhotoVariantFull request and for a
// PhotoVariantThumb request that falls back because ThumbnailRef is nil
// (generation never ran, or failed at upload time — see Upload's own doc).
// No caller of Open/DirectURL ever has to implement this fallback itself.
func (s *PhotoService) resolveRef(photo *domain.Photo, variant domain.PhotoVariant) domain.StorageRef {
	if variant == domain.PhotoVariantThumb && photo.ThumbnailRef != nil {
		return *photo.ThumbnailRef
	}
	return photo.StorageRef
}

// Delete removes photoID (first confirming itemID is visible to viewer and
// that photoID is attached to itemID), then its stored bytes: the
// PhotoRepository row(s) first, the PhotoStore object(s) last (see
// domain.PhotoRepository's own doc for why item-scoped dedup makes this
// ordering safe with no refcounting) — the thumbnail object (when one
// exists), then the full object (NSTR-84). A missing thumbnail object is
// not an error: PhotoStore.Delete is idempotent on a missing key by the
// contract NSTR-34 locked, which matters here since ThumbnailRef can be nil
// or can point at an object a prior, partially-failed Delete already
// removed.
func (s *PhotoService) Delete(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, photoID domain.PhotoID) error {
	photo, err := s.viewablePhoto(ctx, viewer, itemID, photoID)
	if err != nil {
		return err
	}
	if err := s.photos.Delete(ctx, itemID, photoID); err != nil {
		return err
	}
	if photo.ThumbnailRef != nil {
		if err := s.store.Delete(ctx, *photo.ThumbnailRef); err != nil {
			return fmt.Errorf("media/app: delete stored thumbnail bytes: %w", err)
		}
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

// ListPrimaryThumbRefs satisfies internal/storage/adapter's own unexported
// primaryPhotoRefLister port (Sprint 5 reconciliation R8): the media-side
// half of the cross-context fan-in read storage's list views (item search
// results, a bin's contents) use to render thumbnails without ever LEFT
// JOINing onto media's own tables. Returns each item in itemIDs' primary
// photo, projected onto the narrow storagedomain.PhotoRef both contexts
// agree on — never media's own domain.Photo or domain.PhotoID reaching
// across the boundary. An item with no primary photo is simply absent from
// the returned map, never an error: a thumbnail is a decoration, and its
// absence must never fail a list render.
//
// Deliberately NOT re-verified per item through itemGetter the way every
// other PhotoService method above is (see itemGetter's own doc): itemIDs is
// always already scoped to what viewer may see by the caller's own query
// (SearchVisible/ListInBin), the same "not itself visibility-scoped, the
// caller already scoped it" contract domain.PhotoRepository.ListByItem
// documents for ListForItem's own preceding check. Re-checking per item
// here would turn the fan-in's one call into one query per row, defeating
// the batching R8 asks for ("ONE call per list render, never one per
// row"). The viewer parameter is still part of this method's signature,
// matching every other PhotoService method's shape and leaving room for a
// future caller that has NOT already scoped itemIDs to extend this method
// to enforce it directly.
func (s *PhotoService) ListPrimaryThumbRefs(ctx context.Context, _ identity.Principal, itemIDs []storagedomain.ItemID) (map[storagedomain.ItemID]storagedomain.PhotoRef, error) {
	photoIDs, err := s.photos.ListPrimaryForItems(ctx, itemIDs)
	if err != nil {
		return nil, fmt.Errorf("media/app: list primary photo refs: %w", err)
	}
	refs := make(map[storagedomain.ItemID]storagedomain.PhotoRef, len(photoIDs))
	for itemID, photoID := range photoIDs {
		refs[itemID] = storagedomain.PhotoRef{PhotoID: photoID.String()}
	}
	return refs, nil
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

// tryGenerateThumbnail generates itemID's thumbnail variant from scrubbed
// (the already-scrubbed upload bytes Upload hashed for the full image) and
// stores it under fullHash — the SAME content hash as the full image — so
// item-scoped dedup keeps working for both variants. Returns nil, logged at
// warn level, on either a generation or a storage failure: never fatal to
// Upload (see its own doc) — the household loses a size optimization, never
// a photo.
func (s *PhotoService) tryGenerateThumbnail(ctx context.Context, itemID storagedomain.ItemID, scrubbed []byte, contentType, fullHash string) *domain.StorageRef {
	thumb, err := s.thumbnailer.Thumbnail(scrubbed, contentType)
	if err != nil {
		s.logger.WarnContext(ctx, "media: thumbnail generation failed, photo will serve the full image", "item_id", itemID.String(), "error", err)
		return nil
	}
	thumbMeta := domain.PutMeta{
		ContentHash: fullHash,
		SizeBytes:   int64(len(thumb.Bytes)),
		ContentType: thumb.ContentType,
		Variant:     domain.PhotoVariantThumb,
	}
	ref, err := s.store.Put(ctx, itemID, thumbMeta, bytes.NewReader(thumb.Bytes))
	if err != nil {
		s.logger.WarnContext(ctx, "media: thumbnail storage failed, photo will serve the full image", "item_id", itemID.String(), "error", err)
		return nil
	}
	return &ref
}

// hashBytes returns the hex sha256 of data — Upload's "hash the scrubbed
// bytes" step (Sprint 5 reconciliation R1/R3), run only after
// PhotoScrubber.Scrub has already produced data from the staged upload, so
// the hash (and the storage key PhotoStore.Put derives from it) always
// describes the bytes actually handed to Put, never the raw upload.
func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
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
