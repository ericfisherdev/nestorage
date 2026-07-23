package adapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// itemDetailColumns left-joins bin, location, and app_user (the item's
// holder) onto item's own columns — FindVisibleDetail's one query for the
// names the bare Item entity (ids only) does not carry: the current bin's
// own name/code, its location's name, or (for a checked-out item) the
// holder's display name/color. LEFT, not INNER, on every join: a held item
// has neither a bin nor a location, and an in-bin item has no holder — the
// same LEFT JOIN shape itemVisibleColumns uses for the visibility predicate
// itself.
const itemDetailColumns = `
	SELECT i.id, i.name, i.description, i.quantity, i.current_bin_id, i.held_by, i.created_by,
	       i.placement_changed_at, i.created_at, i.updated_at,
	       b.name, b.code, l.name, au.display_name, au.color
	FROM item i
	LEFT JOIN bin b ON b.id = i.current_bin_id
	LEFT JOIN location l ON l.id = b.location_id
	LEFT JOIN app_user au ON au.id = i.held_by`

// itemSearchColumns is SearchVisible's own version of itemDetailColumns,
// selecting only the columns the results list renders (see
// domain.ItemSearchResult) rather than the full detail projection.
const itemSearchColumns = `
	SELECT i.id, i.name, i.quantity, i.current_bin_id, i.held_by,
	       b.code, l.name, au.display_name
	FROM item i
	LEFT JOIN bin b ON b.id = i.current_bin_id
	LEFT JOIN location l ON l.id = b.location_id
	LEFT JOIN app_user au ON au.id = i.held_by`

// FindVisibleDetail returns id's detail read model, scoped to what viewer
// may see (itemVisibilityWhere's own rule, including the held-item
// exception). Returns domain.ErrItemNotFound both when id is unknown and
// when the item exists but viewer may not see it.
func (r *ItemRepository) FindVisibleDetail(ctx context.Context, viewer identity.Principal, id domain.ItemID) (*domain.ItemDetailResult, error) {
	q := itemDetailColumns + ` WHERE i.id = $1 AND ` + itemVisibilityWhere(1)
	args := append([]any{id.String()}, viewerArgs(viewer)...)

	result, err := scanItemDetail(r.dbtx.QueryRow(ctx, q, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrItemNotFound
		}
		return nil, fmt.Errorf("find visible item detail: %w", err)
	}
	return result, nil
}

// SearchVisible returns every item viewer may see whose own name/
// description, its bin's name, or its location's name contains query
// (case-insensitive substring, accelerated by the item_name_trgm/
// item_description_trgm/bin_name_trgm/location_name_trgm GIN indexes —
// 00009_item_search.sql), ordered by name and tie-broken by id, capped at
// limit rows. A held item (no bin/location to join) matches only on its own
// name/description, since its b.name/l.name columns are NULL for that row.
// Returns an empty slice, not an error, when nothing matches.
func (r *ItemRepository) SearchVisible(ctx context.Context, viewer identity.Principal, query string, limit int) ([]domain.ItemSearchResult, error) {
	pattern := "%" + escapeLikeTerm(query) + "%"
	q := itemSearchColumns + ` WHERE ` + itemVisibilityWhere(0) + `
		AND (i.name ILIKE $3 OR i.description ILIKE $3 OR b.name ILIKE $3 OR l.name ILIKE $3)
		ORDER BY i.name, i.id
		LIMIT $4`
	args := append(viewerArgs(viewer), pattern, limit)

	rows, err := r.dbtx.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("search visible items: %w", err)
	}
	defer rows.Close()

	results := make([]domain.ItemSearchResult, 0)
	for rows.Next() {
		result, err := scanItemSearchResult(rows)
		if err != nil {
			return nil, fmt.Errorf("search visible items: scan: %w", err)
		}
		results = append(results, *result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search visible items: %w", err)
	}
	return results, nil
}

// escapeLikeTerm escapes a user-supplied search term's LIKE/ILIKE
// metacharacters — the backslash escape character itself, then % and _ —
// before it is wrapped in %…% and bound as a query parameter, so a literal
// underscore or percent sign in an item/bin/location name is matched
// literally rather than treated as a wildcard. Postgres' default LIKE/ILIKE
// escape character is backslash (verified against the PostgreSQL 16
// functions-matching docs), so no explicit ESCAPE clause is needed at the
// call site above.
func escapeLikeTerm(term string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(term)
}

// scanItemDetail scans one itemDetailColumns row into an *domain.ItemDetailResult,
// kept in lockstep with itemDetailColumns' own column list, mirroring
// scanItem's shape for the joined columns it additionally carries.
func scanItemDetail(r scanner) (*domain.ItemDetailResult, error) {
	var (
		it                             domain.Item
		idStr, createdByStr            string
		description                    *string
		currentBinStr, heldByStr       *string
		binName, binCode, locationName *string
		holderName, holderColor        *string
	)
	if err := r.Scan(
		&idStr, &it.Name, &description, &it.Quantity, &currentBinStr, &heldByStr, &createdByStr,
		&it.PlacementChangedAt, &it.CreatedAt, &it.UpdatedAt,
		&binName, &binCode, &locationName, &holderName, &holderColor,
	); err != nil {
		return nil, err
	}

	id, err := domain.ParseItemID(idStr)
	if err != nil {
		return nil, fmt.Errorf("scan item detail: id: %w", err)
	}
	createdBy, err := identity.ParseUserID(createdByStr)
	if err != nil {
		return nil, fmt.Errorf("scan item detail: created by: %w", err)
	}
	it.ID, it.CreatedBy, it.Description = id, createdBy, description

	if currentBinStr != nil {
		binID, err := domain.ParseBinID(*currentBinStr)
		if err != nil {
			return nil, fmt.Errorf("scan item detail: current bin id: %w", err)
		}
		it.CurrentBinID = &binID
	}
	if heldByStr != nil {
		heldBy, err := identity.ParseUserID(*heldByStr)
		if err != nil {
			return nil, fmt.Errorf("scan item detail: held by: %w", err)
		}
		it.HeldBy = &heldBy
	}

	result := &domain.ItemDetailResult{Item: it}
	if binName != nil {
		result.BinName = *binName
	}
	if binCode != nil {
		result.BinCode = *binCode
	}
	if locationName != nil {
		result.LocationName = *locationName
	}
	if holderName != nil {
		result.HolderName = *holderName
	}
	if holderColor != nil {
		result.HolderColor = identity.UserColor(*holderColor)
	}
	return result, nil
}

// scanItemSearchResult scans one itemSearchColumns row into an
// *domain.ItemSearchResult, kept in lockstep with itemSearchColumns' own
// column list.
func scanItemSearchResult(r scanner) (*domain.ItemSearchResult, error) {
	var (
		idStr                             string
		name                              string
		quantity                          int
		currentBinStr, heldByStr          *string
		binCode, locationName, holderName *string
	)
	if err := r.Scan(&idStr, &name, &quantity, &currentBinStr, &heldByStr, &binCode, &locationName, &holderName); err != nil {
		return nil, err
	}
	id, err := domain.ParseItemID(idStr)
	if err != nil {
		return nil, fmt.Errorf("scan item search result: id: %w", err)
	}

	result := &domain.ItemSearchResult{ID: id, Name: name, Quantity: quantity}
	switch {
	case heldByStr != nil:
		result.State = domain.StateCheckedOut
	case currentBinStr != nil:
		result.State = domain.StateInBin
	}
	if binCode != nil {
		result.BinCode = *binCode
	}
	if locationName != nil {
		result.LocationName = *locationName
	}
	if holderName != nil {
		result.HolderName = *holderName
	}
	return result, nil
}
