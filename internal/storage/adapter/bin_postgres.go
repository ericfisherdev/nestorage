package adapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ericfisherdev/nestcore/db"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// uniqueViolation is the PostgreSQL SQLSTATE for a unique-constraint
// violation.
const uniqueViolation = "23505"

// Explicit constraint names from 00007_bin.sql. Named (unlike
// device_token_user_id_fkey's Postgres-assigned default) so Create can tell
// apart three differently-shaped foreign keys, not just one.
const (
	binCodeUniqueConstraint  = "bin_code_uniq"
	binLocationFKConstraint  = "bin_location_id_fkey"
	binOwnerFKConstraint     = "bin_owner_id_fkey"
	binCreatedByFKConstraint = "bin_created_by_fkey"
)

// binColumns is shared by every read query, keeping the column list and
// scanBin in lockstep.
const binColumns = `SELECT id, code, name, description, location_id, owner_id, created_by, visibility, created_at, updated_at FROM bin`

// BinRepository is the pgx-backed domain.BinRepository. UUIDs are passed and
// scanned as text, matching the location adapter, so no pgx UUID codec
// registration is required.
type BinRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.BinRepository = (*BinRepository)(nil)

// NewBinRepository constructs the repository with an injected query
// executor.
func NewBinRepository(dbtx db.TX) *BinRepository {
	if dbtx == nil {
		panic("storage/adapter: NewBinRepository requires a non-nil db.TX")
	}
	return &BinRepository{dbtx: dbtx}
}

// Create inserts a bin and populates its CreatedAt/UpdatedAt. An unset
// b.Visibility defaults to domain.VisibilityPublic — written back onto b —
// satisfying "bins default to public on creation" without relying solely on
// the column's own DEFAULT. The caller supplies a normalized+validated code
// (see domain.NormalizeBinCode and domain.Bin.Validate); Create does not
// re-normalize or re-validate on write, the same contract
// LocationRepository.Create documents for name.
func (r *BinRepository) Create(ctx context.Context, b *domain.Bin) error {
	if b == nil {
		return errors.New("storage/adapter: create bin: nil bin")
	}
	visibility := b.Visibility
	if visibility == "" {
		visibility = domain.VisibilityPublic
	}
	const q = `
		INSERT INTO bin (id, code, name, description, location_id, owner_id, created_by, visibility)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at, updated_at`
	err := r.dbtx.QueryRow(ctx, q,
		b.ID.String(), b.Code, b.Name, b.Description, b.LocationID.String(),
		userIDParam(b.OwnerID), b.CreatedBy.String(), visibility.String(),
	).Scan(&b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		switch {
		case isPgConstraint(err, uniqueViolation, binCodeUniqueConstraint):
			return domain.ErrDuplicateBinCode
		case isPgConstraint(err, foreignKeyViolation, binLocationFKConstraint):
			return domain.ErrLocationNotFound
		case isPgConstraint(err, foreignKeyViolation, binOwnerFKConstraint),
			isPgConstraint(err, foreignKeyViolation, binCreatedByFKConstraint):
			return identity.ErrUserNotFound
		}
		return fmt.Errorf("create bin: %w", err)
	}
	b.Visibility = visibility
	return nil
}

// FindVisibleByID returns the bin, scoped to what viewer may see (see
// visibilityWhere). Returns domain.ErrBinNotFound both when id is unknown
// and when the bin exists but viewer may not see it.
func (r *BinRepository) FindVisibleByID(ctx context.Context, viewer identity.Principal, id domain.BinID) (*domain.Bin, error) {
	q := binColumns + ` WHERE id = $1 AND ` + visibilityWhere(1)
	args := append([]any{id.String()}, viewerArgs(viewer)...)

	b, err := scanBin(r.dbtx.QueryRow(ctx, q, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrBinNotFound
		}
		return nil, fmt.Errorf("find visible bin by id: %w", err)
	}
	return b, nil
}

// FindVisibleByCode returns the bin whose code matches, scoped to what
// viewer may see. code is normalized before the lookup (see
// domain.NormalizeBinCode) since a scanned label's case cannot be trusted.
// Returns domain.ErrBinNotFound both when code is unknown and when the bin
// exists but viewer may not see it.
func (r *BinRepository) FindVisibleByCode(ctx context.Context, viewer identity.Principal, code string) (*domain.Bin, error) {
	q := binColumns + ` WHERE code = $1 AND ` + visibilityWhere(1)
	args := append([]any{domain.NormalizeBinCode(code)}, viewerArgs(viewer)...)

	b, err := scanBin(r.dbtx.QueryRow(ctx, q, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrBinNotFound
		}
		return nil, fmt.Errorf("find visible bin by code: %w", err)
	}
	return b, nil
}

// ListVisible returns every bin viewer may see, ordered by code. Returns an
// empty slice, not an error, when none are visible.
func (r *BinRepository) ListVisible(ctx context.Context, viewer identity.Principal) ([]domain.Bin, error) {
	q := binColumns + ` WHERE ` + visibilityWhere(0) + ` ORDER BY code`

	rows, err := r.dbtx.Query(ctx, q, viewerArgs(viewer)...)
	if err != nil {
		return nil, fmt.Errorf("list visible bins: %w", err)
	}
	defer rows.Close()

	bins := make([]domain.Bin, 0)
	for rows.Next() {
		b, err := scanBin(rows)
		if err != nil {
			return nil, fmt.Errorf("list visible bins: scan: %w", err)
		}
		bins = append(bins, *b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list visible bins: %w", err)
	}
	return bins, nil
}

// UpdateVisibility overwrites id's visibility, scoped to what viewer may
// mutate (see visibilityWhere — today the same predicate FindVisibleByID
// reads with, mirroring CanMutateBin's own doc that it is the same rule as
// CanSeeBin today but kept separate for a later ticket to tighten). Returns
// domain.ErrBinNotFound when id is unknown or not mutable by viewer.
func (r *BinRepository) UpdateVisibility(ctx context.Context, viewer identity.Principal, id domain.BinID, visibility domain.Visibility) error {
	q := `UPDATE bin SET visibility = $2, updated_at = now() WHERE id = $1 AND ` + visibilityWhere(2)
	args := append([]any{id.String(), visibility.String()}, viewerArgs(viewer)...)

	tag, err := r.dbtx.Exec(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("update bin visibility: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrBinNotFound
	}
	return nil
}

// Delete removes the bin, scoped to what viewer may mutate (see
// visibilityWhere). Returns domain.ErrBinNotFound when id is unknown or not
// mutable by viewer, or domain.ErrBinNotEmpty when a dependent row (an item,
// NSTR-28) still references it — enforced at the database by
// item.current_bin_id's ON DELETE RESTRICT foreign key, the bin-side analog
// of LocationRepository.Delete's ErrLocationNotEmpty.
func (r *BinRepository) Delete(ctx context.Context, viewer identity.Principal, id domain.BinID) error {
	q := `DELETE FROM bin WHERE id = $1 AND ` + visibilityWhere(1)
	args := append([]any{id.String()}, viewerArgs(viewer)...)

	tag, err := r.dbtx.Exec(ctx, q, args...)
	if err != nil {
		if isForeignKeyViolation(err) {
			return domain.ErrBinNotEmpty
		}
		return fmt.Errorf("delete bin: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrBinNotFound
	}
	return nil
}

// visibilityWhere returns the SQL fragment enforcing identity.CanSeeBin's
// (and, today, identically, CanMutateBin's) exact rule — public, the
// viewer's own bin, or an admin viewer — parameterized starting at
// paramOffset+1 (the viewer id) and paramOffset+2 (whether the viewer is an
// admin). viewerArgs supplies the two arguments in that same order; every
// caller appends viewerArgs(viewer) immediately after its own params so the
// placeholder numbers this returns can never drift out of sync with the
// values bound to them. Threading paramOffset through every BinRepository
// method that reads or mutates a specific bin is what lets this one
// fragment stay identical everywhere it is used; bin_test.go's
// CanSeeBin-agreement matrix is what keeps it honest against the Go rule it
// mirrors.
func visibilityWhere(paramOffset int) string {
	return fmt.Sprintf("(visibility = 'public' OR created_by = $%d OR $%d)", paramOffset+1, paramOffset+2)
}

// viewerArgs returns the two arguments visibilityWhere's placeholders bind
// to, in order: the viewer's user id (the zero UUID for a non-user
// principal, which matches no real bin's created_by) and whether the viewer
// is an admin.
func viewerArgs(viewer identity.Principal) []any {
	return []any{viewer.UserID.String(), viewer.IsAdmin()}
}

// userIDParam converts a nullable *identity.UserID into a query parameter:
// nil stays nil (NULL), a set id becomes its string form. Mirrors
// parentIDParam's rationale for *domain.LocationID.
func userIDParam(id *identity.UserID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

// isPgConstraint reports whether err is a Postgres error with the given
// SQLSTATE code and constraint name, generalizing
// isDeviceTokenUserFKViolation's single-constraint check to the three named
// foreign keys and one unique constraint bin's Create must distinguish.
func isPgConstraint(err error, code, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code && pgErr.ConstraintName == constraint
}

func scanBin(r scanner) (*domain.Bin, error) {
	var (
		bin                              domain.Bin
		idStr, locationStr, createdByStr string
		ownerStr                         *string
		visibilityStr                    string
	)
	if err := r.Scan(
		&idStr, &bin.Code, &bin.Name, &bin.Description, &locationStr, &ownerStr, &createdByStr,
		&visibilityStr, &bin.CreatedAt, &bin.UpdatedAt,
	); err != nil {
		return nil, err
	}

	id, err := domain.ParseBinID(idStr)
	if err != nil {
		return nil, fmt.Errorf("scan bin: id: %w", err)
	}
	locationID, err := domain.ParseLocationID(locationStr)
	if err != nil {
		return nil, fmt.Errorf("scan bin: location id: %w", err)
	}
	createdBy, err := identity.ParseUserID(createdByStr)
	if err != nil {
		return nil, fmt.Errorf("scan bin: created by: %w", err)
	}
	visibility, err := domain.ParseVisibility(visibilityStr)
	if err != nil {
		return nil, fmt.Errorf("scan bin: %w", err)
	}
	bin.ID, bin.LocationID, bin.CreatedBy, bin.Visibility = id, locationID, createdBy, visibility

	if ownerStr != nil {
		ownerID, err := identity.ParseUserID(*ownerStr)
		if err != nil {
			return nil, fmt.Errorf("scan bin: owner id: %w", err)
		}
		bin.OwnerID = &ownerID
	}
	return &bin, nil
}
