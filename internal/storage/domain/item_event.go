package domain

import (
	"context"
	"fmt"
	"strings"
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// EventKind is the append-only item event log's event type — one of the
// nine values item_event_kind_check permits. Typed (rather than a bare
// string) so an unknown kind is rejected at parse time, mirroring
// identity.Kind's own rationale.
type EventKind string

// The nine kinds ItemEventRepository.Append accepts, mirroring
// item_event_kind_check exactly. There is deliberately no
// "return request fulfilled" kind: fulfilment happens inside the same
// transaction as a placement change that already emits EventAdded or
// EventReturned, so a tenth kind would double-write one physical action
// (see 00012_item_event.sql's own comment).
const (
	// EventCreated is written when an item is first created (NSTR-41's
	// ItemService.Create).
	EventCreated EventKind = "created"
	// EventAdded is written when an item is placed into a bin (NSTR-41's
	// OperationService.AddToBin). BinID/BinLabel carry the destination bin.
	EventAdded EventKind = "added"
	// EventRemoved is written when an item is checked out of a bin
	// (NSTR-41's OperationService.RemoveFromBin). BinID/BinLabel carry the
	// bin the item left, not the item's new (held) location — the same
	// snapshot NSTR-42's bin activity panel and timeline both read.
	EventRemoved EventKind = "removed"
	// EventReturned is written when a held item is returned to a bin
	// (NSTR-41's OperationService.ReturnToBin, including NSTR-43's
	// fulfilment flows). BinID/BinLabel carry the destination bin.
	EventReturned EventKind = "returned"
	// EventMoved means the bin holding this item was relocated to a
	// different location (NSTR-41's BinMover.Move, fanned out one event per
	// item in the bin) — the item's own bin does not change. BinID/BinLabel
	// carry the containing bin, unchanged by the move; FromLocationID/
	// FromLocationLabel/ToLocationID/ToLocationLabel carry the location
	// change. This is NOT a direct item bin-to-bin move — that is a
	// distinct, not-yet-built feature that will claim its own kind rather
	// than reuse this one.
	EventMoved EventKind = "moved"
	// EventDeleted is written when an item is deleted (NSTR-41's
	// ItemService.Delete), inside the same transaction that removes the
	// item row — item_id/bin_id carry no foreign key specifically so this
	// event can outlive that transaction.
	EventDeleted EventKind = "deleted"
	// EventReturnRequested is written when someone asks for a held item
	// back (NSTR-43's ReturnRequestService), appended directly rather than
	// flowing through OperationService, since requesting is not a placement
	// operation. BinID/BinLabel are NULL.
	EventReturnRequested EventKind = "return_requested"
	// EventReturnRequestCancelled is written when a return request is
	// cancelled (NSTR-43's ReturnRequestService), the EventReturnRequested
	// counterpart. BinID/BinLabel are NULL.
	EventReturnRequestCancelled EventKind = "return_request_cancelled"
	// EventEdited is written when an item's own editable fields (name,
	// description, quantity) change — not a placement change and not a
	// lifecycle event, so BinID/BinLabel are NULL. ChangedFields names which
	// fields changed; ItemName snapshots the name AT EVENT TIME, so an
	// EventEdited event that renamed the item carries the new name and the
	// previous name is on the preceding event. As of this ticket,
	// ItemService.Edit has no production caller (see the ticket's own
	// note) — the kind, column, and CHECK are defined and unit-tested ahead
	// of a caller, the same position EventCreated/EventAdded/EventDeleted
	// were already in before NSTR-41.
	EventEdited EventKind = "edited"
)

// Valid reports whether k is one of the nine known EventKind values.
func (k EventKind) Valid() bool {
	switch k {
	case EventCreated, EventAdded, EventRemoved, EventReturned, EventMoved, EventDeleted,
		EventReturnRequested, EventReturnRequestCancelled, EventEdited:
		return true
	default:
		return false
	}
}

// String returns the kind's stored value.
func (k EventKind) String() string { return string(k) }

// ParseEventKind validates and returns an EventKind, or a wrapped
// ErrInvalidEventKind naming the offending value — this is what "unknown
// event kinds are rejected at parse time" hangs on.
func ParseEventKind(s string) (EventKind, error) {
	k := EventKind(s)
	if !k.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidEventKind, s)
	}
	return k, nil
}

// EditedField is one of the three item fields an EventEdited event can name
// in ChangedFields — the domain mirror of item_event_edit_check's
// changed_fields <@ ARRAY['name','description','quantity'] clause.
type EditedField string

// The three fields ItemService.Edit can change, matching item_event's
// changed_fields CHECK exactly.
const (
	FieldName        EditedField = "name"
	FieldDescription EditedField = "description"
	FieldQuantity    EditedField = "quantity"
)

// Valid reports whether f is one of the three known EditedField values.
func (f EditedField) Valid() bool {
	switch f {
	case FieldName, FieldDescription, FieldQuantity:
		return true
	default:
		return false
	}
}

// String returns the field's stored value.
func (f EditedField) String() string { return string(f) }

// ParseEditedField validates and returns an EditedField, or a wrapped
// ErrInvalidEditedField naming the offending value, mirroring
// ParseEventKind's own shape.
func ParseEditedField(s string) (EditedField, error) {
	f := EditedField(s)
	if !f.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidEditedField, s)
	}
	return f, nil
}

// ItemEvent is one row in the append-only item event log: a single fact
// about an item's lifecycle, attributed to whichever principal caused it.
// BinID/BinLabel, the four location snapshot fields, and ChangedFields are
// each populated only for the EventKind that uses them (see item_event's
// own migration comment and each EventKind constant's doc) — Validate
// enforces the same pairing the database CHECKs enforce.
//
// BinLabel, FromLocationLabel, and ToLocationLabel are plain strings, not
// pointers: each pairs with a nullable id field (BinID, FromLocationID,
// ToLocationID) under an all-or-nothing rule, so the id field alone already
// carries the "is this set" signal and the label is "" exactly when its id
// is nil.
type ItemEvent struct {
	ID          ItemEventID
	HouseholdID identity.HouseholdID
	ItemID      ItemID
	ItemName    string
	Kind        EventKind
	ActorKind   identity.Kind
	ActorUserID identity.UserID
	ActorLabel  string

	BinID    *BinID
	BinLabel string

	Note string

	FromLocationID    *LocationID
	FromLocationLabel string
	ToLocationID      *LocationID
	ToLocationLabel   string

	ChangedFields []EditedField

	OccurredAt time.Time
	CreatedAt  time.Time
}

// NewItemEvent constructs an ItemEvent attributed to actor — the single
// place a Principal's Kind/UserID/Label map onto the stored
// actor_kind/actor_user_id/actor_label columns, so every caller (present and
// future, across all three credentials) resolves attribution identically.
// ActorUserID is left the zero identity.UserID for a KindIntegration actor,
// matching item_event_actor_check's NULL-only-for-integration rule.
//
// The caller sets any kind-specific fields (BinID/BinLabel, the location
// snapshot fields, ChangedFields, Note) directly on the returned value and
// calls Validate before Append — the same "construct, then set, then
// validate" shape domain.Item's callers already follow.
func NewItemEvent(id ItemEventID, itemID ItemID, itemName string, kind EventKind, actor identity.Principal) ItemEvent {
	e := ItemEvent{
		ID:          id,
		HouseholdID: actor.HouseholdID,
		ItemID:      itemID,
		ItemName:    itemName,
		Kind:        kind,
		ActorKind:   actor.Kind,
		ActorLabel:  actor.Actor(),
	}
	if actor.Kind == identity.KindUser {
		e.ActorUserID = actor.UserID
	}
	return e
}

// Validate reports whether e is well-formed, wrapping ErrInvalidItemEvent:
// a known Kind, non-blank ItemName/ActorLabel, an actor/bin/move/edit field
// pairing that matches the matching item_event CHECK — run before Append
// ever reaches the database.
func (e *ItemEvent) Validate() error {
	if !e.Kind.Valid() {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidItemEvent, e.Kind)
	}
	if strings.TrimSpace(e.ItemName) == "" {
		return fmt.Errorf("%w: item name is required", ErrInvalidItemEvent)
	}
	if strings.TrimSpace(e.ActorLabel) == "" {
		return fmt.Errorf("%w: actor label is required", ErrInvalidItemEvent)
	}
	if err := e.validateActor(); err != nil {
		return err
	}
	if (e.BinID == nil) != (e.BinLabel == "") {
		return fmt.Errorf("%w: bin id and bin label must be set together", ErrInvalidItemEvent)
	}
	if err := e.validateMove(); err != nil {
		return err
	}
	return e.validateEdit()
}

// validateActor mirrors item_event_actor_check: a KindUser actor requires a
// set ActorUserID, a KindIntegration actor requires a zero one, and any
// other ActorKind is rejected outright.
func (e *ItemEvent) validateActor() error {
	switch e.ActorKind {
	case identity.KindUser:
		if e.ActorUserID == (identity.UserID{}) {
			return fmt.Errorf("%w: a user actor requires an actor user id", ErrInvalidItemEvent)
		}
	case identity.KindIntegration:
		if e.ActorUserID != (identity.UserID{}) {
			return fmt.Errorf("%w: an integration actor must not carry an actor user id", ErrInvalidItemEvent)
		}
	default:
		return fmt.Errorf("%w: unknown actor kind %q", ErrInvalidItemEvent, e.ActorKind)
	}
	return nil
}

// validateMove mirrors item_event_move_check: all four location snapshot
// fields set for EventMoved, all four unset otherwise.
func (e *ItemEvent) validateMove() error {
	allSet := e.FromLocationID != nil && e.FromLocationLabel != "" && e.ToLocationID != nil && e.ToLocationLabel != ""
	allUnset := e.FromLocationID == nil && e.FromLocationLabel == "" && e.ToLocationID == nil && e.ToLocationLabel == ""
	switch {
	case e.Kind == EventMoved && !allSet:
		return fmt.Errorf("%w: a moved event requires all four location snapshot fields", ErrInvalidItemEvent)
	case e.Kind != EventMoved && !allUnset:
		return fmt.Errorf("%w: only a moved event may carry location snapshot fields", ErrInvalidItemEvent)
	}
	return nil
}

// validateEdit mirrors item_event_edit_check: a non-empty ChangedFields, all
// valid EditedField values, for EventEdited; an empty ChangedFields
// otherwise.
func (e *ItemEvent) validateEdit() error {
	if e.Kind != EventEdited {
		if len(e.ChangedFields) != 0 {
			return fmt.Errorf("%w: only an edited event may carry changed fields", ErrInvalidItemEvent)
		}
		return nil
	}
	if len(e.ChangedFields) == 0 {
		return fmt.Errorf("%w: an edited event requires at least one changed field", ErrInvalidItemEvent)
	}
	for _, f := range e.ChangedFields {
		if !f.Valid() {
			return fmt.Errorf("%w: unknown changed field %q", ErrInvalidItemEvent, f)
		}
	}
	return nil
}

// HistoryCursor identifies a position in a newest-first ListByItem page: the
// (occurred_at, id) pair the keyset query pages on. The id (a UUIDv7,
// time-ordered but not identical to occurred_at) is the tie-break that
// keeps pages stable when two events share an occurred_at.
type HistoryCursor struct {
	OccurredAt time.Time
	ID         ItemEventID
}

// HistoryPage bounds a ListByItem call: at most Limit rows, strictly older
// than Before (nil for the first page).
type HistoryPage struct {
	Limit  int
	Before *HistoryCursor
}

// ItemEventRepository is the outbound port for NSTR-40's append-only item
// event log. Implementations live in the adapter package, bound to a
// nestcore db.TX so NSTR-41's writers can append inside the same
// transaction as the placement change they record.
//
// The interface has no update or delete method — the port is append-only by
// shape, the schema by privilege and trigger (see 00012_item_event.sql).
//
// Persistence contracts:
//   - Append expects a Validate-passing *ItemEvent with e.ID set by the
//     caller (domain.NewItemEventID); it populates OccurredAt/CreatedAt
//     from the database's own now().
//   - ListByItem returns itemID's events newest-first (occurred_at DESC, id
//     DESC), at most page.Limit rows, strictly older than page.Before when
//     set. Returns an empty slice, not an error, when itemID has no events.
//   - ListByBin returns binID's events newest-first (occurred_at DESC, id
//     DESC), at most page.Limit rows, strictly older than page.Before when
//     set — the same keyset shape ListByItem already implements. NSTR-42's
//     bin activity panel still only ever requests a fixed first page (no
//     Before) at its own binActivityLimit; NSTR-55's own bin history API
//     endpoint is what actually pages through Before. Because EventRemoved
//     carries the bin the item left (not only EventAdded the bin it
//     entered), this surfaces removals as well as additions. Returns an
//     empty slice, not an error, when binID has no events.
//
// Error contracts:
//   - Append returns a wrapped identity.ErrUserNotFound when actor_user_id
//     is unknown (item_event_actor_user_id_fkey) — the only foreign key
//     this table carries; item_id and bin_id have none (see the migration's
//     own comment) so an unknown item/bin id is never rejected here.
type ItemEventRepository interface {
	Append(ctx context.Context, e *ItemEvent) error
	ListByItem(ctx context.Context, itemID ItemID, page HistoryPage) ([]ItemEvent, error)
	ListByBin(ctx context.Context, binID BinID, page HistoryPage) ([]ItemEvent, error)
}
