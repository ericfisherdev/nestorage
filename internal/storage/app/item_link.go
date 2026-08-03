package app

import (
	"context"
	"fmt"
	"log/slog"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// itemLinkRepository is the narrow port (ISP) ItemLinkService depends on,
// satisfied by domain.ItemLinkRepository (a superset) and by test fakes.
type itemLinkRepository interface {
	Create(ctx context.Context, householdID identity.HouseholdID, l *domain.ItemLink) error
	Update(ctx context.Context, householdID identity.HouseholdID, itemID domain.ItemID, id domain.ItemLinkID, label, rawURL string) error
	Delete(ctx context.Context, householdID identity.HouseholdID, itemID domain.ItemID, id domain.ItemLinkID) error
	ListByItem(ctx context.Context, householdID identity.HouseholdID, itemID domain.ItemID) ([]domain.ItemLink, error)
	NextPosition(ctx context.Context, householdID identity.HouseholdID, itemID domain.ItemID) (int, error)
}

// itemLinkItemGetter is the narrow port (ISP) ItemLinkService depends on to
// authorize every operation against the owning item's visibility, satisfied
// by domain.ItemRepository (a superset, via Get) and by test fakes. Mirrors
// media/app.itemGetter's identical rationale: a labeled URL carries no
// visibility of its own, so every method below resolves itemID through this
// port FIRST and propagates domain.ErrItemNotFound when viewer may not see
// it, before ever touching a link — the masking contract that stops a
// caller from adding, editing, or deleting links on an item they cannot see.
type itemLinkItemGetter interface {
	Get(ctx context.Context, viewer identity.Principal, id domain.ItemID) (*domain.Item, error)
}

// ItemLinkService implements NSTR-38's labeled-URL management: add, edit,
// remove, and list, every one of them scoped to what viewer may see via
// itemLinkItemGetter.
type ItemLinkService struct {
	links  itemLinkRepository
	items  itemLinkItemGetter
	logger *slog.Logger
}

// NewItemLinkService constructs ItemLinkService. Every dependency is
// required; a missing one panics at construction time, matching every other
// constructor in this codebase (see NewItemService).
func NewItemLinkService(links itemLinkRepository, items itemLinkItemGetter, logger *slog.Logger) *ItemLinkService {
	if links == nil {
		panic("storage/app: NewItemLinkService requires a non-nil itemLinkRepository")
	}
	if items == nil {
		panic("storage/app: NewItemLinkService requires a non-nil itemLinkItemGetter")
	}
	if logger == nil {
		panic("storage/app: NewItemLinkService requires a non-nil logger")
	}
	return &ItemLinkService{links: links, items: items, logger: logger}
}

// Add validates label/rawURL, confirms itemID is visible to viewer, assigns
// the link the item's next position, and persists it. Returns
// domain.ErrItemLinkLabelRequired/ErrItemLinkLabelTooLong/
// ErrInvalidItemLinkURL from validation, or a wrapped domain.ErrItemNotFound
// when itemID is unknown or not visible to viewer.
func (s *ItemLinkService) Add(ctx context.Context, viewer identity.Principal, itemID domain.ItemID, label, rawURL string) (*domain.ItemLink, error) {
	if err := domain.ValidateItemLinkLabel(label); err != nil {
		return nil, err
	}
	validURL, err := domain.ValidateItemLinkURL(rawURL)
	if err != nil {
		return nil, err
	}
	if _, err := s.items.Get(ctx, viewer, itemID); err != nil {
		return nil, fmt.Errorf("app: add item link: %w", err)
	}

	position, err := s.links.NextPosition(ctx, viewer.HouseholdID, itemID)
	if err != nil {
		return nil, fmt.Errorf("app: add item link: next position: %w", err)
	}

	l := &domain.ItemLink{
		ID:       domain.NewItemLinkID(),
		ItemID:   itemID,
		Label:    label,
		URL:      validURL,
		Position: position,
	}
	if err := s.links.Create(ctx, viewer.HouseholdID, l); err != nil {
		return nil, fmt.Errorf("app: add item link: %w", err)
	}
	s.logAction(ctx, "item link added", itemID, l.ID)
	return l, nil
}

// Edit validates label/rawURL, confirms itemID is visible to viewer, and
// overwrites linkID's label/url. Returns the same validation errors Add
// does, plus a wrapped domain.ErrItemNotFound (unknown/invisible item) or
// domain.ErrItemLinkNotFound (unknown link, or one belonging to another
// item).
func (s *ItemLinkService) Edit(ctx context.Context, viewer identity.Principal, itemID domain.ItemID, linkID domain.ItemLinkID, label, rawURL string) error {
	if err := domain.ValidateItemLinkLabel(label); err != nil {
		return err
	}
	validURL, err := domain.ValidateItemLinkURL(rawURL)
	if err != nil {
		return err
	}
	if _, err := s.items.Get(ctx, viewer, itemID); err != nil {
		return fmt.Errorf("app: edit item link: %w", err)
	}
	if err := s.links.Update(ctx, viewer.HouseholdID, itemID, linkID, label, validURL); err != nil {
		return fmt.Errorf("app: edit item link: %w", err)
	}
	s.logAction(ctx, "item link edited", itemID, linkID)
	return nil
}

// Remove confirms itemID is visible to viewer, then deletes linkID. Returns
// a wrapped domain.ErrItemNotFound or domain.ErrItemLinkNotFound.
func (s *ItemLinkService) Remove(ctx context.Context, viewer identity.Principal, itemID domain.ItemID, linkID domain.ItemLinkID) error {
	if _, err := s.items.Get(ctx, viewer, itemID); err != nil {
		return fmt.Errorf("app: remove item link: %w", err)
	}
	if err := s.links.Delete(ctx, viewer.HouseholdID, itemID, linkID); err != nil {
		return fmt.Errorf("app: remove item link: %w", err)
	}
	s.logAction(ctx, "item link removed", itemID, linkID)
	return nil
}

// ListForItem returns itemID's links, ordered by position, first confirming
// itemID is visible to viewer. Returns a wrapped domain.ErrItemNotFound when
// itemID is unknown or not visible to viewer; an empty slice, not an error,
// when the item has no links.
func (s *ItemLinkService) ListForItem(ctx context.Context, viewer identity.Principal, itemID domain.ItemID) ([]domain.ItemLink, error) {
	if _, err := s.items.Get(ctx, viewer, itemID); err != nil {
		return nil, fmt.Errorf("app: list item links: %w", err)
	}
	links, err := s.links.ListByItem(ctx, viewer.HouseholdID, itemID)
	if err != nil {
		return nil, fmt.Errorf("app: list item links: %w", err)
	}
	return links, nil
}

// logAction writes one INFO-level audit line for a completed link mutation.
// It logs the item and link ids, never the label or URL — Nestorage's
// PII-out-of-logs convention (see ItemService.logAction), which also keeps
// an arbitrary user-supplied URL out of the log stream.
func (s *ItemLinkService) logAction(ctx context.Context, msg string, itemID domain.ItemID, linkID domain.ItemLinkID) {
	s.logger.InfoContext(ctx, "storage: "+msg, "item_id", itemID.String(), "link_id", linkID.String())
}
