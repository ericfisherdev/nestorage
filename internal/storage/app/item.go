// Package app contains the storage context's application services.
// ItemService orchestrates item creation, edits, and visibility-aware
// reads/list/delete over domain.ItemRepository, without depending on any
// infrastructure package directly. NSTR-29 adds OperationService
// (operations.go): the transactional add/remove/return placement
// operations, over the same domain.ItemRepository/domain.BinRepository
// shape but through its own narrower ports.
package app

import (
	"context"
	"fmt"
	"log/slog"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// itemRepository is the narrow port (ISP) ItemService depends on: only the
// methods item management actually calls, satisfied by domain.ItemRepository
// (a superset) and by test fakes. Move is deliberately absent — NSTR-29's
// add/remove/return operations call it directly, not through this service.
type itemRepository interface {
	Create(ctx context.Context, it *domain.Item) error
	Get(ctx context.Context, viewer identity.Principal, id domain.ItemID) (*domain.Item, error)
	Update(ctx context.Context, it *domain.Item) error
	ListByBin(ctx context.Context, viewer identity.Principal, binID domain.BinID) ([]domain.Item, error)
	Delete(ctx context.Context, id domain.ItemID) error
}

// ItemService implements item management: create, edit, and visibility-aware
// read/list, plus delete. Add/remove/return (checking an item in or out) are
// NSTR-29's work against domain.ItemRepository.Move, not this service's.
type ItemService struct {
	items  itemRepository
	logger *slog.Logger
}

// NewItemService constructs ItemService. Both dependencies are required; a
// missing one panics at construction time, matching every other constructor
// in this codebase (see identity/app.NewAdminService).
func NewItemService(items itemRepository, logger *slog.Logger) *ItemService {
	if items == nil {
		panic("storage/app: NewItemService requires a non-nil itemRepository")
	}
	if logger == nil {
		panic("storage/app: NewItemService requires a non-nil logger")
	}
	return &ItemService{items: items, logger: logger}
}

// Create validates and persists a new item placed in binID, attributed to
// creator. Returns domain.ErrItemNameRequired/ErrInvalidQuantity/
// ErrInvalidPlacement from validation, or a wrapped domain.ErrBinNotFound/
// identity.ErrUserNotFound from the repository's foreign-key checks.
func (s *ItemService) Create(ctx context.Context, name string, description *string, quantity int, binID domain.BinID, creator identity.UserID) (*domain.Item, error) {
	it := &domain.Item{
		ID:           domain.NewItemID(),
		Name:         name,
		Description:  description,
		Quantity:     quantity,
		CurrentBinID: &binID,
		CreatedBy:    creator,
	}
	if err := it.Validate(); err != nil {
		return nil, err
	}
	if err := s.items.Create(ctx, it); err != nil {
		return nil, fmt.Errorf("app: create item: %w", err)
	}
	s.logAction(ctx, "item created", it.ID)
	return it, nil
}

// Edit overwrites id's name, description, and quantity — never placement or
// creator (see domain.ItemRepository.Update's own doc). Returns
// domain.ErrItemNameRequired/ErrInvalidQuantity from validation, or a
// wrapped domain.ErrItemNotFound when id is unknown.
func (s *ItemService) Edit(ctx context.Context, id domain.ItemID, name string, description *string, quantity int) error {
	if err := domain.ValidateItemName(name); err != nil {
		return err
	}
	if err := domain.ValidateItemQuantity(quantity); err != nil {
		return err
	}
	it := &domain.Item{ID: id, Name: name, Description: description, Quantity: quantity}
	if err := s.items.Update(ctx, it); err != nil {
		return fmt.Errorf("app: edit item: %w", err)
	}
	s.logAction(ctx, "item edited", id)
	return nil
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

// Delete removes id. Returns a wrapped domain.ErrItemNotFound when id is
// unknown. Not visibility-scoped at this layer either — see
// domain.ItemRepository.Delete's own doc; a caller exposing this to an
// end user is responsible for authorizing the request first (typically via
// a preceding Get).
func (s *ItemService) Delete(ctx context.Context, id domain.ItemID) error {
	if err := s.items.Delete(ctx, id); err != nil {
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
