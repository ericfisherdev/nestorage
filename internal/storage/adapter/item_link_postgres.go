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

// itemLinkItemFKConstraint is 00011_item_link.sql's named foreign key, let
// Create tell an unknown item_id apart from any other insert failure —
// mirrors itemCurrentBinFKConstraint's own rationale (item_postgres.go).
const itemLinkItemFKConstraint = "item_link_item_id_fkey"

// item_link carries no household_id column of its own
// (00018_household_scoping.sql's own comment: it is an ON DELETE CASCADE
// child scoped entirely through the item it belongs to), so every read below
// joins item to apply the household predicate instead of selecting from a
// shared column-list constant.

// ItemLinkRepository is the pgx-backed domain.ItemLinkRepository. UUIDs are
// passed and scanned as text, matching every other adapter in this package,
// so no pgx UUID codec registration is required.
type ItemLinkRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.ItemLinkRepository = (*ItemLinkRepository)(nil)

// NewItemLinkRepository constructs the repository with an injected query
// executor.
func NewItemLinkRepository(dbtx db.TX) *ItemLinkRepository {
	if dbtx == nil {
		panic("storage/adapter: NewItemLinkRepository requires a non-nil db.TX")
	}
	return &ItemLinkRepository{dbtx: dbtx}
}

// Create inserts a link and populates its CreatedAt/UpdatedAt. The caller
// supplies a validated link (see domain.ValidateItemLinkLabel/
// ValidateItemLinkURL); Create does not re-validate label/url in Go, relying
// on the database CHECKs, but still maps an unknown or cross-household
// item_id back to domain.ErrItemNotFound. The INSERT ... SELECT ... WHERE
// EXISTS shape is what lets Create confirm l.ItemID belongs to householdID
// in the same statement, since item_link itself carries no household_id
// column to check a plain INSERT's foreign key against.
func (r *ItemLinkRepository) Create(ctx context.Context, householdID identity.HouseholdID, l *domain.ItemLink) error {
	if l == nil {
		return errors.New("storage/adapter: create item link: nil link")
	}
	const q = `
		INSERT INTO item_link (id, item_id, label, url, position)
		SELECT $1, $2, $3, $4, $5
		WHERE EXISTS (SELECT 1 FROM item WHERE id = $2 AND household_id = $6)
		RETURNING created_at, updated_at`
	err := r.dbtx.QueryRow(ctx, q, l.ID.String(), l.ItemID.String(), l.Label, l.URL, l.Position, householdID.String()).
		Scan(&l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || isPgConstraint(err, foreignKeyViolation, itemLinkItemFKConstraint) {
			return domain.ErrItemNotFound
		}
		return fmt.Errorf("create item link: %w", err)
	}
	return nil
}

// Update overwrites the (itemID, id) link's label and url, scoped to
// householdID via a join against item. Returns domain.ErrItemLinkNotFound
// when no row matches — including when id belongs to a different item, or
// itemID belongs to a different household.
func (r *ItemLinkRepository) Update(ctx context.Context, householdID identity.HouseholdID, itemID domain.ItemID, id domain.ItemLinkID, label, rawURL string) error {
	const q = `
		UPDATE item_link SET label = $3, url = $4, updated_at = now()
		WHERE item_id = $1 AND id = $2
		  AND EXISTS (SELECT 1 FROM item WHERE item.id = item_link.item_id AND item.household_id = $5)`
	tag, err := r.dbtx.Exec(ctx, q, itemID.String(), id.String(), label, rawURL, householdID.String())
	if err != nil {
		return fmt.Errorf("update item link: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrItemLinkNotFound
	}
	return nil
}

// Delete removes the (itemID, id) link, scoped to householdID via a join
// against item. Returns domain.ErrItemLinkNotFound when no row matches.
func (r *ItemLinkRepository) Delete(ctx context.Context, householdID identity.HouseholdID, itemID domain.ItemID, id domain.ItemLinkID) error {
	const q = `
		DELETE FROM item_link
		WHERE item_id = $1 AND id = $2
		  AND EXISTS (SELECT 1 FROM item WHERE item.id = item_link.item_id AND item.household_id = $3)`
	tag, err := r.dbtx.Exec(ctx, q, itemID.String(), id.String(), householdID.String())
	if err != nil {
		return fmt.Errorf("delete item link: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrItemLinkNotFound
	}
	return nil
}

// ListByItem returns itemID's links ordered by position, tie-broken by id,
// scoped to householdID via a join against item. Returns an empty slice, not
// an error, when itemID has no links, is unknown, or belongs to a different
// household.
func (r *ItemLinkRepository) ListByItem(ctx context.Context, householdID identity.HouseholdID, itemID domain.ItemID) ([]domain.ItemLink, error) {
	const q = `
		SELECT il.id, il.item_id, il.label, il.url, il.position, il.created_at, il.updated_at
		FROM item_link il
		JOIN item i ON i.id = il.item_id
		WHERE il.item_id = $1 AND i.household_id = $2
		ORDER BY il.position, il.id`
	rows, err := r.dbtx.Query(ctx, q, itemID.String(), householdID.String())
	if err != nil {
		return nil, fmt.Errorf("list item links: %w", err)
	}
	defer rows.Close()

	links := make([]domain.ItemLink, 0)
	for rows.Next() {
		l, err := scanItemLink(rows)
		if err != nil {
			return nil, fmt.Errorf("list item links: scan: %w", err)
		}
		links = append(links, *l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list item links: %w", err)
	}
	return links, nil
}

// NextPosition returns one past itemID's current highest link position (0
// when it has none yet, is unknown, or belongs to a different household).
// COALESCE covers the "no rows yet" case: MAX over zero rows is SQL NULL,
// which COALESCE turns into -1 so +1 yields 0.
func (r *ItemLinkRepository) NextPosition(ctx context.Context, householdID identity.HouseholdID, itemID domain.ItemID) (int, error) {
	const q = `
		SELECT COALESCE(MAX(il.position), -1) + 1
		FROM item_link il
		JOIN item i ON i.id = il.item_id
		WHERE il.item_id = $1 AND i.household_id = $2`
	var next int
	if err := r.dbtx.QueryRow(ctx, q, itemID.String(), householdID.String()).Scan(&next); err != nil {
		return 0, fmt.Errorf("next item link position: %w", err)
	}
	return next, nil
}

func scanItemLink(r scanner) (*domain.ItemLink, error) {
	var (
		l                domain.ItemLink
		idStr, itemIDStr string
	)
	if err := r.Scan(&idStr, &itemIDStr, &l.Label, &l.URL, &l.Position, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return nil, err
	}

	id, err := domain.ParseItemLinkID(idStr)
	if err != nil {
		return nil, fmt.Errorf("scan item link: id: %w", err)
	}
	itemID, err := domain.ParseItemID(itemIDStr)
	if err != nil {
		return nil, fmt.Errorf("scan item link: item id: %w", err)
	}
	l.ID, l.ItemID = id, itemID
	return &l, nil
}
