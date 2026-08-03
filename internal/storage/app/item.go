// Package app contains the storage context's application services.
// ItemService orchestrates item creation, edits, and visibility-aware
// reads/list/delete over domain.ItemRepository, without depending on any
// infrastructure package directly. NSTR-29 adds OperationService
// (operations.go): the transactional add/remove/return placement
// operations, over the same domain.ItemRepository/domain.BinRepository
// shape but through its own narrower ports. NSTR-30 adds BinMover
// (mover.go): the transactional bin-relocation operation. NSTR-31 adds
// LocationService (location.go) and BinService (bin.go): the read-oriented
// services the browse UI's web handlers consume, so a handler never reaches
// into a domain.*Repository (or identity's) directly. NSTR-41 wraps
// Create/Edit/Delete in their own transactional seam (itemTransactor below)
// so each mutation and the item_event row it emits (created/edited/deleted)
// commit or roll back together.
package app

import (
	"context"
	"fmt"
	"log/slog"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// itemRepository is the narrow port (ISP) ItemService depends on for its
// non-transactional reads: Get, ListInBin, and — as of NSTR-54 —
// ListVisible. Move, and — as of NSTR-41 — Create/Update/Delete are
// deliberately absent: NSTR-29's add/remove/return operations call Move
// directly, and Create/Update/Delete now run inside itemTransactor's
// transactional seam (ItemLifecycleStore below), not through this port.
type itemRepository interface {
	Get(ctx context.Context, viewer identity.Principal, id domain.ItemID) (*domain.Item, error)
	ListByBin(ctx context.Context, viewer identity.Principal, binID domain.BinID) ([]domain.Item, error)
	ListVisible(ctx context.Context, viewer identity.Principal, f domain.ItemFilter) ([]domain.Item, error)
}

// ItemLifecycleStore is the narrow port (ISP) ItemTxStores.Items exposes to
// an itemTransactor's transactional closure: Create and Delete for the two
// lifecycle mutations, GetForUpdate and Update for Edit's read-modify-write
// (NSTR-41's Sprint 6 decision to emit an edited event: Edit must read the
// current row before diffing it against the incoming values). Both already
// exist on domain.ItemRepository (a superset) — this widens the store
// interface with no new repository work. Exported, unlike itemRepository,
// because adapter.PostgresUnitOfWork must spell it by name to construct
// ItemTxStores.
type ItemLifecycleStore interface {
	Create(ctx context.Context, it *domain.Item) error
	GetForUpdate(ctx context.Context, householdID identity.HouseholdID, id domain.ItemID) (*domain.Item, error)
	Update(ctx context.Context, householdID identity.HouseholdID, it *domain.Item) error
	Delete(ctx context.Context, householdID identity.HouseholdID, id domain.ItemID) error
}

// ItemTxStores bundles the tx-bound stores an itemTransactor's closure
// operates on: ItemLifecycleStore for the mutation, EventAppender for the
// item_event row that records it in the same transaction. A struct bundle
// (NSTR-41's reconciliation R1), mirroring OperationStores/BinTxStores'
// own rationale (operations.go/mover.go). Exported because
// adapter.PostgresUnitOfWork must spell it by name to satisfy
// itemTransactor.
type ItemTxStores struct {
	Items  ItemLifecycleStore
	Events EventAppender
}

// itemTransactor runs fn inside one transaction against an ItemTxStores
// bundle, committing when fn returns nil and rolling back — with no
// partial write, including the item_event row fn appended — on any error
// fn returns. The item-lifecycle analog of transactor/binTransactor
// (operations.go/mover.go). Named for its single WithinItemTx method, per
// the same single-method-interface convention transactor/binTransactor
// follow.
type itemTransactor interface {
	WithinItemTx(ctx context.Context, fn func(ItemTxStores) error) error
}

// ItemService implements item management: create, edit, and visibility-aware
// read/list, plus delete. Add/remove/return (checking an item in or out) are
// NSTR-29's work against domain.ItemRepository.Move, not this service's.
// Create/Edit/Delete have no production caller as of NSTR-41 (see Create's
// own doc) — the transactional seam and event emission are built and
// unit-tested ahead of the caller that reaches them (Sprint 8's public API).
type ItemService struct {
	items  itemRepository
	uow    itemTransactor
	logger *slog.Logger
}

// NewItemService constructs ItemService. Every dependency is required; a
// missing one panics at construction time, matching every other constructor
// in this codebase (see identity/app.NewAdminService).
func NewItemService(items itemRepository, uow itemTransactor, logger *slog.Logger) *ItemService {
	if items == nil {
		panic("storage/app: NewItemService requires a non-nil itemRepository")
	}
	if uow == nil {
		panic("storage/app: NewItemService requires a non-nil itemTransactor")
	}
	if logger == nil {
		panic("storage/app: NewItemService requires a non-nil logger")
	}
	return &ItemService{items: items, uow: uow, logger: logger}
}

// Create validates and persists a new item placed in binID, attributed to
// actor, appending an EventCreated row in the same transaction. Returns
// domain.ErrItemNameRequired/ErrInvalidQuantity/ErrInvalidPlacement from
// validation, or a wrapped domain.ErrBinNotFound/identity.ErrUserNotFound
// from the repository's foreign-key checks.
//
// As of NSTR-41, Create has zero production callers (verified against the
// merged tree: ItemService is constructed once, at cmd/server/main.go, and
// consumed only through the read-only itemLister/itemQueryService ports —
// see NSTR-41's own reconciliation R9). It becomes reachable once Sprint
// 8's public JSON API calls it; the seam and its event are built and
// tested ahead of that caller, the same position EventAdded/EventRemoved
// were already in before this ticket.
func (s *ItemService) Create(ctx context.Context, name string, description *string, quantity int, binID domain.BinID, actor identity.Principal) (*domain.Item, error) {
	it := &domain.Item{
		ID:           domain.NewItemID(),
		HouseholdID:  actor.HouseholdID,
		Name:         name,
		Description:  description,
		Quantity:     quantity,
		CurrentBinID: &binID,
		CreatedBy:    actor.UserID,
	}
	if err := it.Validate(); err != nil {
		return nil, err
	}

	err := s.uow.WithinItemTx(ctx, func(stores ItemTxStores) error {
		if err := stores.Items.Create(ctx, it); err != nil {
			return err
		}
		event := domain.NewItemEvent(domain.NewItemEventID(), it.ID, it.Name, domain.EventCreated, actor)
		if err := event.Validate(); err != nil {
			return err
		}
		return stores.Events.Append(ctx, &event)
	})
	if err != nil {
		return nil, fmt.Errorf("app: create item: %w", err)
	}
	s.logAction(ctx, "item created", it.ID)
	return it, nil
}

// Edit overwrites id's name, description, and quantity — never placement or
// creator (see domain.ItemRepository.Update's own doc) — and, per NSTR-41's
// Sprint 6 decision, appends an EventEdited row naming exactly the fields
// that differed from the current row. Edit always writes (Update runs
// unconditionally, matching its pre-NSTR-41 behavior); the event is what is
// conditional: an edit that changes nothing appends no event and is not an
// error — "the log records changes, not requests to change." Returns
// domain.ErrItemNameRequired/ErrInvalidQuantity from validation, or a
// wrapped domain.ErrItemNotFound when id is unknown.
func (s *ItemService) Edit(ctx context.Context, id domain.ItemID, name string, description *string, quantity int, actor identity.Principal) error {
	if err := domain.ValidateItemName(name); err != nil {
		return err
	}
	if err := domain.ValidateItemQuantity(quantity); err != nil {
		return err
	}

	err := s.uow.WithinItemTx(ctx, func(stores ItemTxStores) error {
		current, err := stores.Items.GetForUpdate(ctx, actor.HouseholdID, id)
		if err != nil {
			return err
		}
		changed := diffEditableFields(current, name, description, quantity)

		updated := &domain.Item{ID: id, Name: name, Description: description, Quantity: quantity}
		if err := stores.Items.Update(ctx, actor.HouseholdID, updated); err != nil {
			return err
		}
		if len(changed) == 0 {
			return nil
		}

		event := domain.NewItemEvent(domain.NewItemEventID(), id, name, domain.EventEdited, actor)
		event.ChangedFields = changed
		if err := event.Validate(); err != nil {
			return err
		}
		return stores.Events.Append(ctx, &event)
	})
	if err != nil {
		return fmt.Errorf("app: edit item: %w", err)
	}
	s.logAction(ctx, "item edited", id)
	return nil
}

// diffEditableFields compares current against the incoming name/
// description/quantity and returns exactly the domain.EditedField values
// that differ, in stable name/description/quantity order — the
// changed_fields NSTR-41's edited event carries. A description pair with
// both nil, or both set to equal strings, counts as unchanged; nil-vs-set
// or a differing string counts as changed.
func diffEditableFields(current *domain.Item, name string, description *string, quantity int) []domain.EditedField {
	var changed []domain.EditedField
	if current.Name != name {
		changed = append(changed, domain.FieldName)
	}
	if !descriptionsEqual(current.Description, description) {
		changed = append(changed, domain.FieldDescription)
	}
	if current.Quantity != quantity {
		changed = append(changed, domain.FieldQuantity)
	}
	return changed
}

// descriptionsEqual reports whether two optional item descriptions carry
// the same value: both nil, or both set to the identical string.
func descriptionsEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Get returns the item viewer may see. Returns a wrapped
// domain.ErrItemNotFound when id is unknown or not visible to viewer.
func (s *ItemService) Get(ctx context.Context, viewer identity.Principal, id domain.ItemID) (*domain.Item, error) {
	it, err := s.items.Get(ctx, viewer, id)
	if err != nil {
		return nil, fmt.Errorf("app: get item: %w", err)
	}
	return it, nil
}

// ListInBin returns every item in binID viewer may see.
func (s *ItemService) ListInBin(ctx context.Context, viewer identity.Principal, binID domain.BinID) ([]domain.Item, error) {
	items, err := s.items.ListByBin(ctx, viewer, binID)
	if err != nil {
		return nil, fmt.Errorf("app: list items in bin: %w", err)
	}
	return items, nil
}

// ListVisible returns every item viewer may see, optionally narrowed by
// filter — NSTR-54's public API's own list read, the pass-through
// itemRepository.ListVisible's own doc describes in full.
func (s *ItemService) ListVisible(ctx context.Context, viewer identity.Principal, filter domain.ItemFilter) ([]domain.Item, error) {
	items, err := s.items.ListVisible(ctx, viewer, filter)
	if err != nil {
		return nil, fmt.Errorf("app: list visible items: %w", err)
	}
	return items, nil
}

// Delete removes id, attributed to actor, appending an EventDeleted row in
// the same transaction — id.Name is read (GetForUpdate, locked) before the
// row is removed so the event can snapshot it: item_event.item_id carries
// no foreign key specifically so this row outlives the transaction that
// deletes the item (see 00012_item_event.sql's own comment). Returns a
// wrapped domain.ErrItemNotFound when id is unknown. Not visibility-scoped
// at this layer either — see domain.ItemRepository.Delete's own doc; a
// caller exposing this to an end user is responsible for authorizing the
// request first (typically via a preceding Get).
func (s *ItemService) Delete(ctx context.Context, id domain.ItemID, actor identity.Principal) error {
	err := s.uow.WithinItemTx(ctx, func(stores ItemTxStores) error {
		it, err := stores.Items.GetForUpdate(ctx, actor.HouseholdID, id)
		if err != nil {
			return err
		}
		if err := stores.Items.Delete(ctx, actor.HouseholdID, id); err != nil {
			return err
		}
		event := domain.NewItemEvent(domain.NewItemEventID(), id, it.Name, domain.EventDeleted, actor)
		if err := event.Validate(); err != nil {
			return err
		}
		return stores.Events.Append(ctx, &event)
	})
	if err != nil {
		return fmt.Errorf("app: delete item: %w", err)
	}
	s.logAction(ctx, "item deleted", id)
	return nil
}

// logAction writes one INFO-level audit line for a completed item mutation.
// It logs the item's id, never its name or description — Nestorage's
// PII-out-of-logs convention (see identity/app.AdminService.logAction).
func (s *ItemService) logAction(ctx context.Context, msg string, id domain.ItemID, extra ...any) {
	args := append([]any{"item_id", id.String()}, extra...)
	s.logger.InfoContext(ctx, "storage: "+msg, args...)
}
