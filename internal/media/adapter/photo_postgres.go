package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ericfisherdev/nestcore/db"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/media/domain"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// PostgreSQL SQLSTATEs this adapter distinguishes, mirroring
// storage/adapter's identical constants (each bounded context's adapter
// package owns its own copy — see that package's postgres.go/bin_postgres.go
// for the precedent).
const (
	uniqueViolation     = "23505"
	foreignKeyViolation = "23503"
	checkViolation      = "23514"
)

// Explicit constraint names from 00010_item_photo.sql. Named so the adapter
// can distinguish them via pgconn.PgError.ConstraintName instead of parsing
// an error message, mirroring item_postgres.go's identical rationale.
const (
	photoStorageRefUniqConstraint = "photo_storage_ref_uniq"
	photoUploadedByFKConstraint   = "photo_uploaded_by_fkey"
	itemPhotoItemFKConstraint     = "item_photo_item_id_fkey"
	itemPhotoPhotoFKConstraint    = "item_photo_photo_id_fkey"
)

// photoColumns selects a photo's own columns with no join — Create,
// FindByStorageRef. thumb_storage_ref (00010_item_photo.sql, populated by
// NSTR-84) is nullable — scanPhoto reads it into a sql.NullString and
// finishScanPhoto projects that onto Photo.ThumbnailRef.
const photoColumns = `SELECT id, storage_ref, thumb_storage_ref, content_sha256, size_bytes, content_type, storage_backend, uploaded_by, created_at FROM photo`

// photoJoinSelectColumns is photo's own columns, p.-prefixed for a query
// joining onto item_photo — the one list itemPhotoJoinColumns (GetForItem)
// and ListByItem's own extended select (which adds ip.position/
// ip.is_primary) are both built from, so the two can never drift apart on
// which columns a joined photo row carries (this is exactly the class of
// drift that first shipped NSTR-84's thumb_storage_ref column in only one
// of the two).
const photoJoinSelectColumns = `p.id, p.storage_ref, p.thumb_storage_ref, p.content_sha256, p.size_bytes, p.content_type, p.storage_backend, p.uploaded_by, p.created_at`

// itemPhotoJoinColumns left-joins photo's columns onto item_photo, used by
// GetForItem.
const itemPhotoJoinColumns = `
	SELECT ` + photoJoinSelectColumns + `
	FROM photo p
	JOIN item_photo ip ON ip.photo_id = p.id`

// PhotoRepository is the pgx-backed domain.PhotoRepository. UUIDs are passed
// and scanned as text, matching every other Nestorage adapter, so no pgx
// UUID codec registration is required.
type PhotoRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.PhotoRepository = (*PhotoRepository)(nil)

// NewPhotoRepository constructs the repository with an injected query
// executor.
func NewPhotoRepository(dbtx db.TX) *PhotoRepository {
	if dbtx == nil {
		panic("media/adapter: NewPhotoRepository requires a non-nil db.TX")
	}
	return &PhotoRepository{dbtx: dbtx}
}

// Create inserts photo (not yet attached to any item) and populates its
// CreatedAt. Returns domain.ErrDuplicatePhoto when photo.StorageRef
// collides with an existing row, identity.ErrUserNotFound when
// photo.UploadedBy is unknown, or domain.ErrInvalidPhoto for an unexpected
// database CHECK violation (every field is already validated by
// Photo.Validate before Create is ever called; a violation here signals an
// application-layer bug).
func (r *PhotoRepository) Create(ctx context.Context, photo *domain.Photo) error {
	if photo == nil {
		return errors.New("media/adapter: create photo: nil photo")
	}
	const q = `
		INSERT INTO photo (id, storage_ref, thumb_storage_ref, content_sha256, size_bytes, content_type, storage_backend, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at`
	err := r.dbtx.QueryRow(ctx, q,
		photo.ID.String(), photo.StorageRef.String(), nullableStorageRefParam(photo.ThumbnailRef), photo.ContentHash, photo.SizeBytes,
		photo.ContentType, photo.StorageBackend.String(), photo.UploadedBy.String(),
	).Scan(&photo.CreatedAt)
	if err != nil {
		switch {
		case isPgConstraint(err, uniqueViolation, photoStorageRefUniqConstraint):
			return domain.ErrDuplicatePhoto
		case isPgConstraint(err, foreignKeyViolation, photoUploadedByFKConstraint):
			return identity.ErrUserNotFound
		case isCheckViolation(err):
			return domain.ErrInvalidPhoto
		}
		return fmt.Errorf("create photo: %w", err)
	}
	return nil
}

// GetForItem returns the photo, scoped to itemID via the item_photo
// junction (see domain.PhotoRepository.GetForItem's own doc). Returns
// domain.ErrPhotoNotFound both when id is unknown and when it exists but is
// not attached to itemID.
func (r *PhotoRepository) GetForItem(ctx context.Context, itemID storagedomain.ItemID, id domain.PhotoID) (*domain.Photo, error) {
	q := itemPhotoJoinColumns + ` WHERE ip.item_id = $1 AND p.id = $2`
	photo, err := scanPhoto(r.dbtx.QueryRow(ctx, q, itemID.String(), id.String()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrPhotoNotFound
		}
		return nil, fmt.Errorf("get photo for item: %w", err)
	}
	return photo, nil
}

// FindByStorageRef returns the photo carrying ref, or
// domain.ErrPhotoNotFound — the expected "not a duplicate" outcome for a
// genuinely new upload.
func (r *PhotoRepository) FindByStorageRef(ctx context.Context, ref domain.StorageRef) (*domain.Photo, error) {
	photo, err := scanPhoto(r.dbtx.QueryRow(ctx, photoColumns+` WHERE storage_ref = $1`, ref.String()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrPhotoNotFound
		}
		return nil, fmt.Errorf("find photo by storage ref: %w", err)
	}
	return photo, nil
}

// AttachToItem inserts the item_photo row joining photoID to itemID.
// Returns storagedomain.ErrItemNotFound when itemID is unknown, or
// domain.ErrPhotoNotFound when photoID is unknown.
func (r *PhotoRepository) AttachToItem(ctx context.Context, itemID storagedomain.ItemID, photoID domain.PhotoID, position int, isPrimary bool) error {
	const q = `INSERT INTO item_photo (item_id, photo_id, position, is_primary) VALUES ($1, $2, $3, $4)`
	_, err := r.dbtx.Exec(ctx, q, itemID.String(), photoID.String(), position, isPrimary)
	if err != nil {
		switch {
		case isPgConstraint(err, foreignKeyViolation, itemPhotoItemFKConstraint):
			return storagedomain.ErrItemNotFound
		case isPgConstraint(err, foreignKeyViolation, itemPhotoPhotoFKConstraint):
			return domain.ErrPhotoNotFound
		}
		return fmt.Errorf("attach photo to item: %w", err)
	}
	return nil
}

// ListByItem returns itemID's attached photos ordered by position, or an
// empty slice when none are attached.
func (r *PhotoRepository) ListByItem(ctx context.Context, itemID storagedomain.ItemID) ([]domain.ItemPhoto, error) {
	q := `
		SELECT ` + photoJoinSelectColumns + `, ip.position, ip.is_primary
		FROM photo p
		JOIN item_photo ip ON ip.photo_id = p.id
		WHERE ip.item_id = $1
		ORDER BY ip.position`
	rows, err := r.dbtx.Query(ctx, q, itemID.String())
	if err != nil {
		return nil, fmt.Errorf("list item photos: %w", err)
	}
	defer rows.Close()

	items := make([]domain.ItemPhoto, 0)
	for rows.Next() {
		var ip domain.ItemPhoto
		photo, position, isPrimary, err := scanItemPhotoRow(rows)
		if err != nil {
			return nil, fmt.Errorf("list item photos: scan: %w", err)
		}
		ip.Photo, ip.Position, ip.IsPrimary = *photo, position, isPrimary
		items = append(items, ip)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list item photos: %w", err)
	}
	return items, nil
}

// Delete removes the (itemID, id) item_photo row and then id's photo row,
// in that order (see domain.PhotoRepository's own doc for why itemID scopes
// which junction row is removed). Returns domain.ErrPhotoNotFound when id
// is unknown.
func (r *PhotoRepository) Delete(ctx context.Context, itemID storagedomain.ItemID, id domain.PhotoID) error {
	if _, err := r.dbtx.Exec(ctx, `DELETE FROM item_photo WHERE item_id = $1 AND photo_id = $2`, itemID.String(), id.String()); err != nil {
		return fmt.Errorf("delete item photo link: %w", err)
	}
	tag, err := r.dbtx.Exec(ctx, `DELETE FROM photo WHERE id = $1`, id.String())
	if err != nil {
		if isForeignKeyViolation(err) {
			// The item_photo_photo_id_fkey guard tripped: a second junction
			// row still references this photo, which item-scoped dedup
			// should make impossible in practice (see the migration's own
			// comment) — surfaced as ErrInvalidPhoto rather than silently
			// orphaning another item's bytes.
			return fmt.Errorf("%w: photo is still attached to another item", domain.ErrInvalidPhoto)
		}
		return fmt.Errorf("delete photo: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrPhotoNotFound
	}
	return nil
}

// SetPrimary clears itemID's current primary photo (if any) and marks
// photoID primary instead. Returns domain.ErrPhotoNotFound when photoID is
// not attached to itemID.
func (r *PhotoRepository) SetPrimary(ctx context.Context, itemID storagedomain.ItemID, photoID domain.PhotoID) error {
	if _, err := r.dbtx.Exec(ctx, `UPDATE item_photo SET is_primary = false WHERE item_id = $1 AND is_primary = true`, itemID.String()); err != nil {
		return fmt.Errorf("clear primary photo: %w", err)
	}
	tag, err := r.dbtx.Exec(ctx, `UPDATE item_photo SET is_primary = true WHERE item_id = $1 AND photo_id = $2`, itemID.String(), photoID.String())
	if err != nil {
		return fmt.Errorf("set primary photo: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrPhotoNotFound
	}
	return nil
}

// Reorder rewrites itemID's attached photos' positions to match order (see
// domain.PhotoRepository.Reorder's own doc). Implemented as two passes: the
// first moves every photo in order to a temporary position outside the
// valid [0, len(order)) range, the second sets each to its final position —
// a swap directly in place would transiently collide with the
// item_photo_position_uniq constraint (00010_item_photo.sql), and the
// temporary range must stay >= 0 to satisfy item_photo_position_check, so a
// negative-offset trick (the usual way to dodge a unique constraint during
// a reorder) is not available here.
func (r *PhotoRepository) Reorder(ctx context.Context, itemID storagedomain.ItemID, order []domain.PhotoID) error {
	if len(order) == 0 {
		return nil
	}
	const q = `UPDATE item_photo SET position = $3 WHERE item_id = $1 AND photo_id = $2`
	tempBase := len(order)
	for i, photoID := range order {
		if _, err := r.dbtx.Exec(ctx, q, itemID.String(), photoID.String(), tempBase+i); err != nil {
			return fmt.Errorf("reorder item photos: stage: %w", err)
		}
	}
	for i, photoID := range order {
		if _, err := r.dbtx.Exec(ctx, q, itemID.String(), photoID.String(), i); err != nil {
			return fmt.Errorf("reorder item photos: finalize: %w", err)
		}
	}
	return nil
}

// SetThumbnailRef records ref as photoID's thumbnail variant (NSTR-84) — the
// UPDATE cmd/backfill-thumbs runs against a row Create left with a NULL
// thumb_storage_ref (see domain.PhotoRepository.SetThumbnailRef's own doc for
// why this is a separate method from Create's upload-path write). Returns
// domain.ErrPhotoNotFound when photoID is unknown.
func (r *PhotoRepository) SetThumbnailRef(ctx context.Context, photoID domain.PhotoID, ref domain.StorageRef) error {
	tag, err := r.dbtx.Exec(ctx, `UPDATE photo SET thumb_storage_ref = $2 WHERE id = $1`, photoID.String(), ref.String())
	if err != nil {
		return fmt.Errorf("set thumbnail ref: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrPhotoNotFound
	}
	return nil
}

// ListMissingThumbnail returns up to limit photo rows whose thumbnail
// reference is still NULL (see domain.PhotoRepository.ListMissingThumbnail's
// own doc for the resumable-cursor contract cmd/backfill-thumbs relies on).
// The explicit ::uuid cast on afterID matters: this codebase passes UUIDs as
// plain text parameters (never registering a pgx UUID codec — see this
// type's own doc), and an untyped text parameter has no implicit ordering
// operator against the uuid column without it.
func (r *PhotoRepository) ListMissingThumbnail(ctx context.Context, limit int, afterID domain.PhotoID) ([]domain.MissingThumbnailPhoto, error) {
	const q = `
		SELECT p.id, ip.item_id, p.storage_ref, p.content_sha256, p.content_type
		FROM photo p
		JOIN item_photo ip ON ip.photo_id = p.id
		WHERE p.thumb_storage_ref IS NULL AND p.id > $1::uuid
		ORDER BY p.id
		LIMIT $2`
	rows, err := r.dbtx.Query(ctx, q, afterID.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("list photos missing thumbnail: %w", err)
	}
	defer rows.Close()

	out := make([]domain.MissingThumbnailPhoto, 0)
	for rows.Next() {
		var idStr, itemIDStr, refStr, contentHash, contentType string
		if err := rows.Scan(&idStr, &itemIDStr, &refStr, &contentHash, &contentType); err != nil {
			return nil, fmt.Errorf("list photos missing thumbnail: scan: %w", err)
		}
		id, err := domain.ParsePhotoID(idStr)
		if err != nil {
			return nil, fmt.Errorf("list photos missing thumbnail: id: %w", err)
		}
		itemID, err := storagedomain.ParseItemID(itemIDStr)
		if err != nil {
			return nil, fmt.Errorf("list photos missing thumbnail: item id: %w", err)
		}
		out = append(out, domain.MissingThumbnailPhoto{
			ID: id, ItemID: itemID, StorageRef: domain.StorageRef(refStr), ContentHash: contentHash, ContentType: contentType,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list photos missing thumbnail: %w", err)
	}
	return out, nil
}

// ListPrimaryForItems returns each item in itemIDs' primary photo id, keyed
// by item id (see domain.PhotoRepository.ListPrimaryForItems's own doc). An
// empty itemIDs returns an empty map without a round trip. ANY($1) against a
// text[] parameter is the same batched-lookup shape
// ListMissingThumbnail's own ::uuid cast comment describes this codebase
// using instead of a pgx UUID codec — item_id/photo_id are compared as
// plain text here too.
func (r *PhotoRepository) ListPrimaryForItems(ctx context.Context, itemIDs []storagedomain.ItemID) (map[storagedomain.ItemID]domain.PhotoID, error) {
	out := make(map[storagedomain.ItemID]domain.PhotoID, len(itemIDs))
	if len(itemIDs) == 0 {
		return out, nil
	}
	ids := make([]string, len(itemIDs))
	for i, id := range itemIDs {
		ids[i] = id.String()
	}
	const q = `SELECT item_id, photo_id FROM item_photo WHERE item_id = ANY($1) AND is_primary = true`
	rows, err := r.dbtx.Query(ctx, q, ids)
	if err != nil {
		return nil, fmt.Errorf("list primary photos for items: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var itemIDStr, photoIDStr string
		if err := rows.Scan(&itemIDStr, &photoIDStr); err != nil {
			return nil, fmt.Errorf("list primary photos for items: scan: %w", err)
		}
		itemID, err := storagedomain.ParseItemID(itemIDStr)
		if err != nil {
			return nil, fmt.Errorf("list primary photos for items: item id: %w", err)
		}
		photoID, err := domain.ParsePhotoID(photoIDStr)
		if err != nil {
			return nil, fmt.Errorf("list primary photos for items: photo id: %w", err)
		}
		out[itemID] = photoID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list primary photos for items: %w", err)
	}
	return out, nil
}

// scanner abstracts pgx.Row and pgx.Rows for the shared scan helpers.
type scanner interface {
	Scan(dest ...any) error
}

func scanPhoto(r scanner) (*domain.Photo, error) {
	var (
		photo             domain.Photo
		idStr, refStr     string
		thumbRefStr       sql.NullString
		storageBackendStr string
		uploadedByStr     string
	)
	if err := r.Scan(
		&idStr, &refStr, &thumbRefStr, &photo.ContentHash, &photo.SizeBytes, &photo.ContentType,
		&storageBackendStr, &uploadedByStr, &photo.CreatedAt,
	); err != nil {
		return nil, err
	}
	return finishScanPhoto(&photo, idStr, refStr, thumbRefStr, storageBackendStr, uploadedByStr)
}

// scanItemPhotoRow scans one ListByItem row: a photo's own columns plus its
// item_photo position/is_primary.
func scanItemPhotoRow(r scanner) (photo *domain.Photo, position int, isPrimary bool, err error) {
	var (
		p                 domain.Photo
		idStr, refStr     string
		thumbRefStr       sql.NullString
		storageBackendStr string
		uploadedByStr     string
	)
	if err := r.Scan(
		&idStr, &refStr, &thumbRefStr, &p.ContentHash, &p.SizeBytes, &p.ContentType,
		&storageBackendStr, &uploadedByStr, &p.CreatedAt, &position, &isPrimary,
	); err != nil {
		return nil, 0, false, err
	}
	photo, err = finishScanPhoto(&p, idStr, refStr, thumbRefStr, storageBackendStr, uploadedByStr)
	return photo, position, isPrimary, err
}

// finishScanPhoto parses the string-typed columns scanPhoto/scanItemPhotoRow
// read into p's typed fields, shared so the two scan paths stay in lockstep.
// thumbRefStr is thumb_storage_ref (NSTR-84): NULL projects to a nil
// Photo.ThumbnailRef, a non-NULL value to a populated one.
func finishScanPhoto(p *domain.Photo, idStr, refStr string, thumbRefStr sql.NullString, storageBackendStr, uploadedByStr string) (*domain.Photo, error) {
	id, err := domain.ParsePhotoID(idStr)
	if err != nil {
		return nil, fmt.Errorf("scan photo: id: %w", err)
	}
	backend, err := domain.ParseStorageBackend(storageBackendStr)
	if err != nil {
		return nil, fmt.Errorf("scan photo: storage backend: %w", err)
	}
	uploadedBy, err := identity.ParseUserID(uploadedByStr)
	if err != nil {
		return nil, fmt.Errorf("scan photo: uploaded by: %w", err)
	}
	p.ID = id
	p.StorageRef = domain.StorageRef(refStr)
	p.ThumbnailRef = nullableStorageRef(thumbRefStr)
	p.StorageBackend = backend
	p.UploadedBy = uploadedBy
	return p, nil
}

// nullableStorageRef projects a scanned nullable thumb_storage_ref column
// onto Photo.ThumbnailRef's *domain.StorageRef shape: nil when NULL, a
// populated pointer otherwise.
func nullableStorageRef(s sql.NullString) *domain.StorageRef {
	if !s.Valid {
		return nil
	}
	ref := domain.StorageRef(s.String)
	return &ref
}

// nullableStorageRefParam is nullableStorageRef's inverse: the pgx query
// parameter Create binds thumb_storage_ref to. pgx maps a nil *string to
// SQL NULL, and a non-nil one to its pointee's value.
func nullableStorageRefParam(ref *domain.StorageRef) *string {
	if ref == nil {
		return nil
	}
	s := ref.String()
	return &s
}

// isPgConstraint reports whether err is a Postgres error with the given
// SQLSTATE code and constraint name, mirroring storage/adapter's identical
// helper.
func isPgConstraint(err error, code, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code && pgErr.ConstraintName == constraint
}

// isCheckViolation reports whether err is a check-constraint violation of
// any kind, generalizing isForeignKeyViolation's identical shape for
// checkViolation.
func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == checkViolation
}

// isForeignKeyViolation reports whether err is a foreign-key violation of
// any kind, mirroring storage/adapter's identical helper.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation
}
