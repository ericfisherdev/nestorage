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

// foreignKeyViolation is the PostgreSQL SQLSTATE for a foreign-key
// violation.
const foreignKeyViolation = "23503"

// locationColumns is shared by every read query, keeping the column list and
// scanLocation in lockstep.
const locationColumns = `SELECT id, household_id, name, description, parent_id, created_by, created_at, updated_at FROM location`

// LocationRepository is the pgx-backed domain.LocationRepository. UUIDs are
// passed and scanned as text, matching the identity adapter, so no pgx UUID
// codec registration is required.
type LocationRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.LocationRepository = (*LocationRepository)(nil)

// NewLocationRepository constructs the repository with an injected query
// executor.
func NewLocationRepository(dbtx db.TX) *LocationRepository {
	if dbtx == nil {
		panic("storage/adapter: NewLocationRepository requires a non-nil db.TX")
	}
	return &LocationRepository{dbtx: dbtx}
}

// Create inserts a location and populates its CreatedAt/UpdatedAt. The
// caller supplies a validated name (see domain.ValidateLocationName) and a
// valid CreatedBy; an unknown CreatedBy or ParentID surfaces as a wrapped
// foreign-key-violation error rather than a domain sentinel — Create has no
// "not found" case of its own to report.
func (r *LocationRepository) Create(ctx context.Context, l *domain.Location) error {
	if l == nil {
		return errors.New("storage/adapter: create location: nil location")
	}
	const q = `
		INSERT INTO location (id, household_id, name, description, parent_id, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at, updated_at`
	err := r.dbtx.QueryRow(ctx, q,
		l.ID.String(), l.HouseholdID.String(), l.Name, l.Description, parentIDParam(l.ParentID), l.CreatedBy.String(),
	).Scan(&l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create location: %w", err)
	}
	return nil
}

// FindVisibleByID returns the location, scoped to viewer's household — see
// domain.LocationRepository.FindVisibleByID's own doc for why no further
// per-viewer privacy check applies (Location carries no privacy field yet,
// unlike Bin's Visibility). Returns domain.ErrLocationNotFound when id is
// unknown or belongs to a different household.
func (r *LocationRepository) FindVisibleByID(ctx context.Context, viewer identity.Principal, id domain.LocationID) (*domain.Location, error) {
	q := locationColumns + ` WHERE id = $1 AND ` + householdWhere(1)
	args := append([]any{id.String()}, householdArgs(viewer)...)

	l, err := scanLocation(r.dbtx.QueryRow(ctx, q, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrLocationNotFound
		}
		return nil, fmt.Errorf("find visible location by id: %w", err)
	}
	return l, nil
}

// List returns every location in viewer's household, ordered by name,
// tie-broken by id for a stable order between rows sharing a name. Returns
// an empty slice, not an error, when viewer's household has no locations.
func (r *LocationRepository) List(ctx context.Context, viewer identity.Principal) ([]domain.Location, error) {
	q := locationColumns + ` WHERE ` + householdWhere(0) + ` ORDER BY name, id`

	rows, err := r.dbtx.Query(ctx, q, householdArgs(viewer)...)
	if err != nil {
		return nil, fmt.Errorf("list locations: %w", err)
	}
	defer rows.Close()

	locations := make([]domain.Location, 0)
	for rows.Next() {
		l, err := scanLocation(rows)
		if err != nil {
			return nil, fmt.Errorf("list locations: scan: %w", err)
		}
		locations = append(locations, *l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list locations: %w", err)
	}
	return locations, nil
}

// Rename overwrites id's name with a caller-validated name, scoped to
// viewer's household. Returns domain.ErrLocationNotFound when id is unknown
// or belongs to a different household.
func (r *LocationRepository) Rename(ctx context.Context, viewer identity.Principal, id domain.LocationID, name string) error {
	q := `UPDATE location SET name = $2, updated_at = now() WHERE id = $1 AND ` + householdWhere(2)
	args := append([]any{id.String(), name}, householdArgs(viewer)...)

	tag, err := r.dbtx.Exec(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("rename location: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrLocationNotFound
	}
	return nil
}

// Delete removes the location, scoped to viewer's household. Returns
// domain.ErrLocationNotFound when id is unknown or belongs to a different
// household, or domain.ErrLocationNotEmpty when a dependent row (a child
// location today; a bin once NSTR-27 lands) still references it — enforced
// at the database by parent_id's ON DELETE RESTRICT foreign key.
func (r *LocationRepository) Delete(ctx context.Context, viewer identity.Principal, id domain.LocationID) error {
	q := `DELETE FROM location WHERE id = $1 AND ` + householdWhere(1)
	args := append([]any{id.String()}, householdArgs(viewer)...)

	tag, err := r.dbtx.Exec(ctx, q, args...)
	if err != nil {
		if isForeignKeyViolation(err) {
			return domain.ErrLocationNotEmpty
		}
		return fmt.Errorf("delete location: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrLocationNotFound
	}
	return nil
}

// parentIDParam converts a nullable *domain.LocationID into a query
// parameter: nil stays nil (NULL), a set id becomes its string form.
func parentIDParam(id *domain.LocationID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

// isForeignKeyViolation reports whether err is a foreign-key violation of
// any kind. Unlike isDuplicateEmail in the identity adapter, this matches on
// SQLSTATE alone, not on constraint name: every possible referrer on
// delete — a child location now, a bin from NSTR-27 later — means the same
// thing, that the location is not empty.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation
}

// householdWhere returns the SQL fragment scoping a query to a single
// household — household_id = $N — parameterized at paramOffset+1.
// householdArgs supplies the one argument that placeholder binds to; every
// caller appends householdArgs(viewer) (or the bare householdID a
// transaction-bound method carries) immediately after its own params, the
// same paramOffset-threading convention visibilityWhere/viewerArgs establish
// for Bin/Item. NSTR-131's household-scoping sweep: every repository read,
// update, and delete gains this predicate (or the household-only equivalent
// on a table with no separate visibility concept, like Location), so a
// cross-household id is indistinguishable from an unknown one.
func householdWhere(paramOffset int) string {
	return fmt.Sprintf("household_id = $%d", paramOffset+1)
}

// householdArgs returns the one argument householdWhere's placeholder binds
// to: viewer's own household id.
func householdArgs(viewer identity.Principal) []any {
	return []any{viewer.HouseholdID.String()}
}

// scopedArgs appends householdArgs(viewer) then viewerArgs(viewer) onto
// prefix, in that order, the exact sequence a query built as `<own predicates>
// AND householdWhere(offset) AND visibilityWhere(offset+1)` expects its
// arguments in. Every BinRepository method that combines both predicates
// builds its args this way; factored out here (rather than repeating the
// two-append pattern at each call site) so a future edit cannot miscount a
// paramOffset by getting the argument order wrong.
func scopedArgs(prefix []any, viewer identity.Principal) []any {
	args := append(prefix, householdArgs(viewer)...)
	return append(args, viewerArgs(viewer)...)
}

// scanner abstracts pgx.Row and pgx.Rows for the shared scan helper.
type scanner interface {
	Scan(dest ...any) error
}

func scanLocation(r scanner) (*domain.Location, error) {
	var (
		l                             domain.Location
		idStr, householdID, createdBy string
		parentID                      *string
	)
	if err := r.Scan(&idStr, &householdID, &l.Name, &l.Description, &parentID, &createdBy, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return nil, err
	}
	id, err := domain.ParseLocationID(idStr)
	if err != nil {
		return nil, fmt.Errorf("scan location: %w", err)
	}
	hID, err := identity.ParseHouseholdID(householdID)
	if err != nil {
		return nil, fmt.Errorf("scan location: household id: %w", err)
	}
	createdByID, err := identity.ParseUserID(createdBy)
	if err != nil {
		return nil, fmt.Errorf("scan location: %w", err)
	}
	l.ID, l.HouseholdID, l.CreatedBy = id, hID, createdByID
	if parentID != nil {
		pid, err := domain.ParseLocationID(*parentID)
		if err != nil {
			return nil, fmt.Errorf("scan location: parent id: %w", err)
		}
		l.ParentID = &pid
	}
	return &l, nil
}
