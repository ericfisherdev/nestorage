package adapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/ericfisherdev/nestcore/db"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// Explicit constraint name from 00012_item_event.sql. The table's only
// foreign key — item_id and bin_id deliberately carry none, see the
// migration's own comment — so Append needs to distinguish just this one
// violation from any other insert failure.
const itemEventActorUserFKConstraint = "item_event_actor_user_id_fkey"

// itemEventColumns is shared by every read query, keeping the column list
// and scanItemEvent in lockstep.
const itemEventColumns = `
	SELECT id, household_id, item_id, item_name, kind, actor_kind, actor_user_id, actor_label,
	       bin_id, bin_label, note, from_location_id, from_location_label,
	       to_location_id, to_location_label, changed_fields, occurred_at, created_at
	FROM item_event`

// ItemEventRepository is the pgx-backed domain.ItemEventRepository. UUIDs
// are passed and scanned as text, matching every other adapter in this
// package, so no pgx UUID codec registration is required. text[] round-trips
// through Go's []string natively (pgx's default type map), so
// changed_fields needs no codec either.
type ItemEventRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.ItemEventRepository = (*ItemEventRepository)(nil)

// NewItemEventRepository constructs the repository with an injected query
// executor. Pass a pgx.Tx (rather than the pool) when the caller needs the
// append to share a transaction with the placement change it records —
// NSTR-41's writers do this, mirroring NewLocationRepository's own doc.
func NewItemEventRepository(dbtx db.TX) *ItemEventRepository {
	if dbtx == nil {
		panic("storage/adapter: NewItemEventRepository requires a non-nil db.TX")
	}
	return &ItemEventRepository{dbtx: dbtx}
}

// Append inserts e and populates its OccurredAt/CreatedAt (both read the
// same insert-time now(), the same pattern item's placement_changed_at/
// created_at/updated_at follows in 00008_item.sql). The caller supplies a
// Validate-passing event; Append does not re-validate kind/actor/bin/move/
// edit pairing in Go, relying on the database CHECKs, but still maps the
// one foreign-key violation this table can produce back to the matching
// domain sentinel.
func (r *ItemEventRepository) Append(ctx context.Context, e *domain.ItemEvent) error {
	if e == nil {
		return errors.New("storage/adapter: append item event: nil event")
	}
	const q = `
		INSERT INTO item_event (
			id, household_id, item_id, item_name, kind, actor_kind, actor_user_id, actor_label,
			bin_id, bin_label, note, from_location_id, from_location_label,
			to_location_id, to_location_label, changed_fields
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING occurred_at, created_at`
	err := r.dbtx.QueryRow(ctx, q,
		e.ID.String(), e.HouseholdID.String(), e.ItemID.String(), e.ItemName, e.Kind.String(), e.ActorKind.String(),
		actorUserIDParam(e.ActorUserID), e.ActorLabel,
		binIDParam(e.BinID), nullableString(e.BinLabel),
		nullableString(e.Note),
		locationIDParam(e.FromLocationID), nullableString(e.FromLocationLabel),
		locationIDParam(e.ToLocationID), nullableString(e.ToLocationLabel),
		changedFieldsParam(e.ChangedFields),
	).Scan(&e.OccurredAt, &e.CreatedAt)
	if err != nil {
		if isPgConstraint(err, foreignKeyViolation, itemEventActorUserFKConstraint) {
			return identity.ErrUserNotFound
		}
		return fmt.Errorf("append item event: %w", err)
	}
	return nil
}

// ListByItem returns itemID's events newest-first (occurred_at DESC, id
// DESC), at most page.Limit rows, strictly older than page.Before when set —
// the keyset page NSTR-42's item timeline reads, and NSTR-55's own item
// history API endpoint. Returns an empty slice, not an error, when itemID
// has no events.
func (r *ItemEventRepository) ListByItem(ctx context.Context, itemID domain.ItemID, page domain.HistoryPage) ([]domain.ItemEvent, error) {
	q, args := buildKeysetHistoryQuery("item_id", itemID.String(), page)
	events, err := queryItemEvents(ctx, r.dbtx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list item events by item: %w", err)
	}
	return events, nil
}

// ListByBin returns binID's events newest-first (occurred_at DESC, id DESC),
// at most page.Limit rows, strictly older than page.Before when set — the
// same keyset shape ListByItem already implements. NSTR-42's bin activity
// panel still only ever passes a page with no Before (a fixed first
// window); NSTR-55's own bin history API endpoint is the first caller to
// actually page through Before. Because EventRemoved carries the bin the
// item left, this surfaces removals as well as additions, not only
// EventAdded. Returns an empty slice, not an error, when binID has no
// events.
func (r *ItemEventRepository) ListByBin(ctx context.Context, binID domain.BinID, page domain.HistoryPage) ([]domain.ItemEvent, error) {
	q, args := buildKeysetHistoryQuery("bin_id", binID.String(), page)
	events, err := queryItemEvents(ctx, r.dbtx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list item events by bin: %w", err)
	}
	return events, nil
}

// buildKeysetHistoryQuery builds the WHERE/ORDER BY/LIMIT clause ListByItem
// and ListByBin both need, differing only in which column scopes the query
// — factored out here rather than left duplicated across the two methods,
// which previously diverged only in that column name and a bare LIMIT
// (ListByBin's own pre-NSTR-55 shape). column is always one of the two
// literals those methods themselves pass, never request input, so building
// it into the query string rather than a placeholder carries no injection
// risk.
func buildKeysetHistoryQuery(column, id string, page domain.HistoryPage) (string, []any) {
	q := itemEventColumns + ` WHERE ` + column + ` = $1`
	args := []any{id}
	if page.Before != nil {
		q += ` AND (occurred_at, id) < ($2, $3)`
		args = append(args, page.Before.OccurredAt, page.Before.ID.String())
	}
	q += fmt.Sprintf(` ORDER BY occurred_at DESC, id DESC LIMIT $%d`, len(args)+1)
	args = append(args, page.Limit)
	return q, args
}

// queryItemEvents runs q (one of ListByItem/ListByBin's queries) and scans
// every row, factored out so the two callers share one scan loop.
func queryItemEvents(ctx context.Context, dbtx db.TX, q string, args ...any) ([]domain.ItemEvent, error) {
	rows, err := dbtx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]domain.ItemEvent, 0)
	for rows.Next() {
		e, err := scanItemEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		events = append(events, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// actorUserIDParam converts identity.UserID into a query parameter: the
// zero UserID (a KindIntegration actor, see item_event_actor_check) becomes
// NULL, a set id becomes its string form. Mirrors binIDParam/userIDParam's
// own nullable-conversion rationale for a value type rather than a pointer.
func actorUserIDParam(id identity.UserID) any {
	if id == (identity.UserID{}) {
		return nil
	}
	return id.String()
}

// locationIDParam converts a nullable *domain.LocationID into a query
// parameter, mirroring binIDParam's own rationale.
func locationIDParam(id *domain.LocationID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

// nullableString converts "" into a query parameter of NULL: ItemEvent's
// BinLabel/Note/FromLocationLabel/ToLocationLabel are plain (non-pointer)
// strings whose "" represents "not set" (see ItemEvent's own doc), and each
// nullable text column must actually store SQL NULL — not the literal empty
// string — for the paired num_nonnulls CHECKs (item_event_bin_check,
// item_event_move_check) to count them correctly.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// changedFieldsParam converts []domain.EditedField into a query parameter:
// an empty/nil slice becomes NULL (item_event_edit_check requires NULL, not
// '{}', for a non-'edited' row), a populated slice becomes []string — pgx's
// default type map encodes []string as a text[] literal directly, the same
// mechanism ListPrimaryForItems' ANY($1) lookup already relies on
// (internal/media/adapter/photo_postgres.go), so no codec registration is
// needed here either.
func changedFieldsParam(fields []domain.EditedField) any {
	if len(fields) == 0 {
		return nil
	}
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = f.String()
	}
	return out
}

func scanItemEvent(r scanner) (*domain.ItemEvent, error) {
	var (
		e                                                     domain.ItemEvent
		idStr, householdStr, itemIDStr, kindStr, actorKindStr string
		actorUserIDStr                                        *string
		binIDStr, binLabel                                    *string
		note                                                  *string
		fromLocationIDStr, fromLocationLabel                  *string
		toLocationIDStr, toLocationLabel                      *string
		changedFields                                         []string
	)
	if err := r.Scan(
		&idStr, &householdStr, &itemIDStr, &e.ItemName, &kindStr, &actorKindStr, &actorUserIDStr, &e.ActorLabel,
		&binIDStr, &binLabel, &note, &fromLocationIDStr, &fromLocationLabel,
		&toLocationIDStr, &toLocationLabel, &changedFields, &e.OccurredAt, &e.CreatedAt,
	); err != nil {
		return nil, err
	}

	id, err := domain.ParseItemEventID(idStr)
	if err != nil {
		return nil, fmt.Errorf("id: %w", err)
	}
	householdID, err := identity.ParseHouseholdID(householdStr)
	if err != nil {
		return nil, fmt.Errorf("household id: %w", err)
	}
	itemID, err := domain.ParseItemID(itemIDStr)
	if err != nil {
		return nil, fmt.Errorf("item id: %w", err)
	}
	kind, err := domain.ParseEventKind(kindStr)
	if err != nil {
		return nil, fmt.Errorf("kind: %w", err)
	}
	actorKind, err := identity.ParseKind(actorKindStr)
	if err != nil {
		return nil, fmt.Errorf("actor kind: %w", err)
	}
	e.ID, e.HouseholdID, e.ItemID, e.Kind, e.ActorKind = id, householdID, itemID, kind, actorKind

	if actorUserIDStr != nil {
		actorUserID, err := identity.ParseUserID(*actorUserIDStr)
		if err != nil {
			return nil, fmt.Errorf("actor user id: %w", err)
		}
		e.ActorUserID = actorUserID
	}
	if binIDStr != nil {
		binID, err := domain.ParseBinID(*binIDStr)
		if err != nil {
			return nil, fmt.Errorf("bin id: %w", err)
		}
		e.BinID = &binID
	}
	if binLabel != nil {
		e.BinLabel = *binLabel
	}
	if note != nil {
		e.Note = *note
	}
	if fromLocationIDStr != nil {
		fromLocationID, err := domain.ParseLocationID(*fromLocationIDStr)
		if err != nil {
			return nil, fmt.Errorf("from location id: %w", err)
		}
		e.FromLocationID = &fromLocationID
	}
	if fromLocationLabel != nil {
		e.FromLocationLabel = *fromLocationLabel
	}
	if toLocationIDStr != nil {
		toLocationID, err := domain.ParseLocationID(*toLocationIDStr)
		if err != nil {
			return nil, fmt.Errorf("to location id: %w", err)
		}
		e.ToLocationID = &toLocationID
	}
	if toLocationLabel != nil {
		e.ToLocationLabel = *toLocationLabel
	}
	for _, f := range changedFields {
		field, err := domain.ParseEditedField(f)
		if err != nil {
			return nil, fmt.Errorf("changed field: %w", err)
		}
		e.ChangedFields = append(e.ChangedFields, field)
	}
	return &e, nil
}
