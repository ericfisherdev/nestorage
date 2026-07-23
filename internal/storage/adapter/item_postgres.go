package adapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ericfisherdev/nestcore/db"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// checkViolation is the PostgreSQL SQLSTATE for a check-constraint
// violation — item is the first storage aggregate with named CHECK
// constraints an adapter needs to distinguish, alongside the existing
// uniqueViolation (bin_postgres.go) and foreignKeyViolation (postgres.go).
const checkViolation = "23514"

// Explicit constraint names from 00008_item.sql. Named (matching bin's own
// explicit foreign keys) so Create and Move can tell apart two differently
// shaped check violations and three differently shaped foreign keys.
const (
	itemQuantityCheckConstraint      = "item_quantity_check"
	itemPlacementExclusiveConstraint = "item_placement_exclusive"
	itemCurrentBinFKConstraint       = "item_current_bin_id_fkey"
	itemHeldByFKConstraint           = "item_held_by_fkey"
	itemCreatedByFKConstraint        = "item_created_by_fkey"
)

// itemColumns selects an item's own columns with no join, used where
// visibility scoping does not apply (GetForUpdate).
const itemColumns = `SELECT id, name, description, quantity, current_bin_id, held_by, created_by, placement_changed_at, created_at, updated_at FROM item`

// itemVisibleColumns left-joins bin so itemVisibilityWhere can apply
// identity.CanSeeBin's rule to current_bin_id — LEFT, not INNER, because a
// held item (current_bin_id NULL) must still be selectable; the held-item
// exception in itemVisibilityWhere is what makes that row visible despite
// the join producing no matching bin.
const itemVisibleColumns = `
	SELECT i.id, i.name, i.description, i.quantity, i.current_bin_id, i.held_by, i.created_by,
	       i.placement_changed_at, i.created_at, i.updated_at
	FROM item i
	LEFT JOIN bin b ON b.id = i.current_bin_id`

// ItemRepository is the pgx-backed domain.ItemRepository. UUIDs are passed
// and scanned as text, matching the location and bin adapters, so no pgx
// UUID codec registration is required.
type ItemRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.ItemRepository = (*ItemRepository)(nil)

// NewItemRepository constructs the repository with an injected query
// executor. Pass a pgx.Tx (rather than the pool) when the caller needs
// GetForUpdate's row lock to hold across multiple statements.
func NewItemRepository(dbtx db.TX) *ItemRepository {
	if dbtx == nil {
		panic("storage/adapter: NewItemRepository requires a non-nil db.TX")
	}
	return &ItemRepository{dbtx: dbtx}
}

// Create inserts an item and populates its PlacementChangedAt/CreatedAt/
// UpdatedAt (all three read the same insert-time now(), see
// 00008_item.sql's own comment). The caller supplies a validated item (see
// domain.Item.Validate); Create does not re-validate name/quantity/
// placement in Go, relying on the database CHECKs, but still maps their
// violations back to the matching domain sentinel.
func (r *ItemRepository) Create(ctx context.Context, it *domain.Item) error {
	if it == nil {
		return errors.New("storage/adapter: create item: nil item")
	}
	const q = `
		INSERT INTO item (id, name, description, quantity, current_bin_id, held_by, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING placement_changed_at, created_at, updated_at`
	err := r.dbtx.QueryRow(ctx, q,
		it.ID.String(), it.Name, it.Description, it.Quantity,
		binIDParam(it.CurrentBinID), userIDParam(it.HeldBy), it.CreatedBy.String(),
	).Scan(&it.PlacementChangedAt, &it.CreatedAt, &it.UpdatedAt)
	if err != nil {
		switch {
		case isPgConstraint(err, checkViolation, itemQuantityCheckConstraint):
			return domain.ErrInvalidQuantity
		case isPgConstraint(err, checkViolation, itemPlacementExclusiveConstraint):
			return domain.ErrInvalidPlacement
		case isPgConstraint(err, foreignKeyViolation, itemCurrentBinFKConstraint):
			return domain.ErrBinNotFound
		case isPgConstraint(err, foreignKeyViolation, itemHeldByFKConstraint),
			isPgConstraint(err, foreignKeyViolation, itemCreatedByFKConstraint):
			return identity.ErrUserNotFound
		}
		return fmt.Errorf("create item: %w", err)
	}
	return nil
}

// Get returns the item, scoped to what viewer may see (see
// itemVisibilityWhere). Returns domain.ErrItemNotFound both when id is
// unknown and when the item exists but viewer may not see it.
func (r *ItemRepository) Get(ctx context.Context, viewer identity.Principal, id domain.ItemID) (*domain.Item, error) {
	q := itemVisibleColumns + ` WHERE i.id = $1 AND ` + itemVisibilityWhere(1)
	args := append([]any{id.String()}, viewerArgs(viewer)...)

	it, err := scanItem(r.dbtx.QueryRow(ctx, q, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrItemNotFound
		}
		return nil, fmt.Errorf("get item: %w", err)
	}
	return it, nil
}

// GetForUpdate returns the item locked FOR UPDATE, not scoped by visibility
// (see domain.ItemRepository.GetForUpdate's own doc: the caller is expected
// to have already checked visibility via Get). Returns
// domain.ErrItemNotFound when id is unknown.
func (r *ItemRepository) GetForUpdate(ctx context.Context, id domain.ItemID) (*domain.Item, error) {
	q := itemColumns + ` WHERE id = $1 FOR UPDATE`
	it, err := scanItem(r.dbtx.QueryRow(ctx, q, id.String()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrItemNotFound
		}
		return nil, fmt.Errorf("get item for update: %w", err)
	}
	return it, nil
}

// Update overwrites id's name, description, and quantity — never placement
// or creator (see domain.ItemRepository's own doc). Returns
// domain.ErrItemNotFound when id is unknown, or
// domain.ErrInvalidQuantity/a wrapped blank-name error when the new values
// violate the database CHECKs.
func (r *ItemRepository) Update(ctx context.Context, it *domain.Item) error {
	if it == nil {
		return errors.New("storage/adapter: update item: nil item")
	}
	const q = `UPDATE item SET name = $2, description = $3, quantity = $4, updated_at = now() WHERE id = $1`
	tag, err := r.dbtx.Exec(ctx, q, it.ID.String(), it.Name, it.Description, it.Quantity)
	if err != nil {
		if isPgConstraint(err, checkViolation, itemQuantityCheckConstraint) {
			return domain.ErrInvalidQuantity
		}
		return fmt.Errorf("update item: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrItemNotFound
	}
	return nil
}

// Move swaps id's current_bin_id/held_by to match dst in one statement,
// advancing placement_changed_at (and updated_at) — see
// domain.ItemRepository.Move's own doc for the rows-affected contract.
// Returns domain.ErrInvalidPlacement immediately, without a round trip,
// when dst itself is malformed.
func (r *ItemRepository) Move(ctx context.Context, id domain.ItemID, dst domain.Placement) (int64, error) {
	if !dst.Valid() {
		return 0, domain.ErrInvalidPlacement
	}
	const q = `
		UPDATE item
		SET current_bin_id = $2, held_by = $3, placement_changed_at = now(), updated_at = now()
		WHERE id = $1`
	tag, err := r.dbtx.Exec(ctx, q, id.String(), binIDParam(dst.BinID), userIDParam(dst.HeldBy))
	if err != nil {
		switch {
		case isPgConstraint(err, checkViolation, itemPlacementExclusiveConstraint):
			return 0, domain.ErrInvalidPlacement
		case isPgConstraint(err, foreignKeyViolation, itemCurrentBinFKConstraint):
			return 0, domain.ErrBinNotFound
		case isPgConstraint(err, foreignKeyViolation, itemHeldByFKConstraint):
			return 0, identity.ErrUserNotFound
		}
		return 0, fmt.Errorf("move item: %w", err)
	}
	affected := tag.RowsAffected()
	if affected == 0 {
		return 0, domain.ErrItemNotFound
	}
	return affected, nil
}

// ListByBin returns every item in binID viewer may see, ordered by name,
// tie-broken by id. Returns an empty slice, not an error, when none are
// visible.
func (r *ItemRepository) ListByBin(ctx context.Context, viewer identity.Principal, binID domain.BinID) ([]domain.Item, error) {
	q := itemVisibleColumns + ` WHERE i.current_bin_id = $1 AND ` + itemVisibilityWhere(1) + ` ORDER BY i.name, i.id`
	args := append([]any{binID.String()}, viewerArgs(viewer)...)

	rows, err := r.dbtx.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list items by bin: %w", err)
	}
	defer rows.Close()

	items := make([]domain.Item, 0)
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("list items by bin: scan: %w", err)
		}
		items = append(items, *it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list items by bin: %w", err)
	}
	return items, nil
}

// Delete removes the item. Returns domain.ErrItemNotFound when id is
// unknown. Not visibility-scoped — see domain.ItemRepository.Delete's own
// doc.
func (r *ItemRepository) Delete(ctx context.Context, id domain.ItemID) error {
	const q = `DELETE FROM item WHERE id = $1`
	tag, err := r.dbtx.Exec(ctx, q, id.String())
	if err != nil {
		return fmt.Errorf("delete item: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrItemNotFound
	}
	return nil
}

// itemVisibilityWhere returns the SQL fragment applying identity.CanSeeBin's
// rule to i.current_bin_id via the LEFT JOIN itemVisibleColumns supplies,
// extended with the held-item exception: a held item (current_bin_id NULL,
// so b.* is all NULL) has no bin to gate on and is always visible.
// Parameterized starting at paramOffset+1/+2, the same viewerArgs contract
// visibilityWhere documents.
func itemVisibilityWhere(paramOffset int) string {
	return fmt.Sprintf(
		"(i.held_by IS NOT NULL OR b.visibility = 'public' OR b.created_by = $%d OR $%d)",
		paramOffset+1, paramOffset+2,
	)
}

// binIDParam converts a nullable *domain.BinID into a query parameter: nil
// stays nil (NULL), a set id becomes its string form. Mirrors userIDParam's
// rationale (bin_postgres.go) for *identity.UserID.
func binIDParam(id *domain.BinID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

func scanItem(r scanner) (*domain.Item, error) {
	var (
		it                       domain.Item
		idStr, createdByStr      string
		description              *string
		currentBinStr, heldByStr *string
	)
	if err := r.Scan(
		&idStr, &it.Name, &description, &it.Quantity, &currentBinStr, &heldByStr, &createdByStr,
		&it.PlacementChangedAt, &it.CreatedAt, &it.UpdatedAt,
	); err != nil {
		return nil, err
	}

	id, err := domain.ParseItemID(idStr)
	if err != nil {
		return nil, fmt.Errorf("scan item: id: %w", err)
	}
	createdBy, err := identity.ParseUserID(createdByStr)
	if err != nil {
		return nil, fmt.Errorf("scan item: created by: %w", err)
	}
	it.ID, it.CreatedBy, it.Description = id, createdBy, description

	if currentBinStr != nil {
		binID, err := domain.ParseBinID(*currentBinStr)
		if err != nil {
			return nil, fmt.Errorf("scan item: current bin id: %w", err)
		}
		it.CurrentBinID = &binID
	}
	if heldByStr != nil {
		heldBy, err := identity.ParseUserID(*heldByStr)
		if err != nil {
			return nil, fmt.Errorf("scan item: held by: %w", err)
		}
		it.HeldBy = &heldBy
	}
	return &it, nil
}
