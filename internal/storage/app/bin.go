package app

import (
	"context"
	"fmt"
	"log/slog"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// binReadWriter is the narrow port (ISP) BinService depends on, satisfied by
// domain.BinRepository (a superset) and by test fakes.
type binReadWriter interface {
	Create(ctx context.Context, b *domain.Bin) error
	FindVisibleByID(ctx context.Context, viewer identity.Principal, id domain.BinID) (*domain.Bin, error)
	FindVisibleByCode(ctx context.Context, viewer identity.Principal, code string) (*domain.Bin, error)
	ListVisible(ctx context.Context, viewer identity.Principal) ([]domain.Bin, error)
	ListVisibleByLocation(ctx context.Context, viewer identity.Principal, locationID domain.LocationID) ([]domain.Bin, error)
	Update(ctx context.Context, viewer identity.Principal, b *domain.Bin) error
	Delete(ctx context.Context, viewer identity.Principal, id domain.BinID) error
}

// itemCounter is the narrow port (ISP) BinService depends on to enrich each
// bin with how many items viewer may see inside it, satisfied by
// domain.ItemRepository (a superset, via CountsByBin) and by test fakes.
type itemCounter interface {
	CountsByBin(ctx context.Context, viewer identity.Principal) (map[domain.BinID]int, error)
}

// BinView is the read model BinService returns: a Bin enriched with its
// owner's display projection (nil for the shared/Family bin) and how many
// items viewer may see inside it, so the web adapter never has to reach
// into identity or re-query items itself to render a bin card.
type BinView struct {
	Bin       domain.Bin
	Owner     *OwnerInfo
	ItemCount int
}

// BinService implements the bin browse/manage operations NSTR-31's web
// handlers consume: owner/count-enriched reads (ListVisible,
// ListVisibleByLocation, GetByCode), and the create/edit/delete commands the
// bin CRUD forms drive. Moving a bin between locations is deliberately not
// here — NSTR-30's app.BinMover already owns that transactional operation,
// and this service must not reimplement it.
type BinService struct {
	bins    binReadWriter
	members memberDirectory
	items   itemCounter
	logger  *slog.Logger
}

// NewBinService constructs BinService. Every dependency is required; a
// missing one panics at construction time, matching every other constructor
// in this codebase (see NewLocationService).
func NewBinService(bins binReadWriter, members memberDirectory, items itemCounter, logger *slog.Logger) *BinService {
	if bins == nil {
		panic("storage/app: NewBinService requires a non-nil binReadWriter")
	}
	if members == nil {
		panic("storage/app: NewBinService requires a non-nil memberDirectory")
	}
	if items == nil {
		panic("storage/app: NewBinService requires a non-nil itemCounter")
	}
	if logger == nil {
		panic("storage/app: NewBinService requires a non-nil logger")
	}
	return &BinService{bins: bins, members: members, items: items, logger: logger}
}

// ListVisible returns every bin viewer may see, each enriched with its
// owner's display projection and visible item count.
func (s *BinService) ListVisible(ctx context.Context, viewer identity.Principal) ([]BinView, error) {
	bins, err := s.bins.ListVisible(ctx, viewer)
	if err != nil {
		return nil, fmt.Errorf("app: list visible bins: %w", err)
	}
	return s.enrich(ctx, viewer, bins)
}

// ListVisibleByLocation returns every bin in locationID viewer may see, each
// enriched the same way ListVisible's result is — the location-detail
// page's bin list.
func (s *BinService) ListVisibleByLocation(ctx context.Context, viewer identity.Principal, locationID domain.LocationID) ([]BinView, error) {
	bins, err := s.bins.ListVisibleByLocation(ctx, viewer, locationID)
	if err != nil {
		return nil, fmt.Errorf("app: list visible bins by location: %w", err)
	}
	return s.enrich(ctx, viewer, bins)
}

// GetByID returns the bin viewer may see, enriched the same way
// ListVisible's result is — the read the id-addressed mutation routes
// (delete, move) re-render their fragment from, since those routes carry a
// domain.BinID (see BinsWebHandlers' own routing), not a code. Returns a
// wrapped domain.ErrBinNotFound both when id is unknown and when the bin
// exists but viewer may not see it.
func (s *BinService) GetByID(ctx context.Context, viewer identity.Principal, id domain.BinID) (*BinView, error) {
	b, err := s.bins.FindVisibleByID(ctx, viewer, id)
	if err != nil {
		return nil, fmt.Errorf("app: get bin by id: %w", err)
	}
	views, err := s.enrich(ctx, viewer, []domain.Bin{*b})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// GetByCode returns the bin whose code matches, scoped to what viewer may
// see, enriched the same way ListVisible's result is. Returns a wrapped
// domain.ErrBinNotFound both when code is unknown and when the bin exists
// but viewer may not see it — so a non-owner's private bin 404s rather than
// 403ing, per the visibility contract's own doc.
func (s *BinService) GetByCode(ctx context.Context, viewer identity.Principal, code string) (*BinView, error) {
	b, err := s.bins.FindVisibleByCode(ctx, viewer, code)
	if err != nil {
		return nil, fmt.Errorf("app: get bin by code: %w", err)
	}
	views, err := s.enrich(ctx, viewer, []domain.Bin{*b})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// Create validates and persists a new bin, normalizing code first (see
// domain.NormalizeBinCode) so a scanned label and a typed one always
// resolve the same way. Returns a wrapped domain.ErrInvalidBin from
// validation, domain.ErrDuplicateBinCode when code is already in use,
// domain.ErrLocationNotFound when locationID is unknown, or
// identity.ErrUserNotFound when ownerID or createdBy is unknown.
func (s *BinService) Create(
	ctx context.Context,
	code, name, description string,
	locationID domain.LocationID,
	ownerID *identity.UserID,
	visibility domain.Visibility,
	createdBy identity.UserID,
) (*domain.Bin, error) {
	b := &domain.Bin{
		ID:          domain.NewBinID(),
		Code:        domain.NormalizeBinCode(code),
		Name:        name,
		Description: description,
		LocationID:  locationID,
		OwnerID:     ownerID,
		Visibility:  visibility,
		CreatedBy:   createdBy,
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	if err := s.bins.Create(ctx, b); err != nil {
		return nil, fmt.Errorf("app: create bin: %w", err)
	}
	s.logAction(ctx, "bin created", b.ID)
	return b, nil
}

// Edit overwrites id's name, description, owner, and visibility — never
// code or location (see domain.BinRepository.Update's own doc). Returns a
// wrapped domain.ErrBinNotFound when id is unknown or not mutable by
// viewer, or identity.ErrUserNotFound when ownerID names an unknown user.
func (s *BinService) Edit(
	ctx context.Context,
	viewer identity.Principal,
	id domain.BinID,
	name, description string,
	ownerID *identity.UserID,
	visibility domain.Visibility,
) error {
	b := &domain.Bin{ID: id, Name: name, Description: description, OwnerID: ownerID, Visibility: visibility}
	if err := s.bins.Update(ctx, viewer, b); err != nil {
		return fmt.Errorf("app: edit bin: %w", err)
	}
	s.logAction(ctx, "bin edited", id)
	return nil
}

// Delete removes id, scoped to what viewer may mutate. Returns a wrapped
// domain.ErrBinNotFound when id is unknown or not mutable by viewer, or
// domain.ErrBinNotEmpty when an item still references it.
func (s *BinService) Delete(ctx context.Context, viewer identity.Principal, id domain.BinID) error {
	if err := s.bins.Delete(ctx, viewer, id); err != nil {
		return fmt.Errorf("app: delete bin: %w", err)
	}
	s.logAction(ctx, "bin deleted", id)
	return nil
}

// enrich projects bins into BinViews: one memberDirectory load and one
// CountsByBin aggregate, shared across every bin in the slice rather than
// queried once per bin.
func (s *BinService) enrich(ctx context.Context, viewer identity.Principal, bins []domain.Bin) ([]BinView, error) {
	members, err := newMemberIndex(ctx, s.members)
	if err != nil {
		return nil, fmt.Errorf("app: load members: %w", err)
	}
	counts, err := s.items.CountsByBin(ctx, viewer)
	if err != nil {
		return nil, fmt.Errorf("app: count items by bin: %w", err)
	}

	views := make([]BinView, 0, len(bins))
	for _, b := range bins {
		views = append(views, BinView{Bin: b, Owner: members.ownerInfo(b.OwnerID), ItemCount: counts[b.ID]})
	}
	return views, nil
}

// logAction writes one INFO-level audit line for a completed bin mutation.
// It logs the bin's id, never its name or description — Nestorage's
// PII-out-of-logs convention (see ItemService.logAction).
func (s *BinService) logAction(ctx context.Context, msg string, id domain.BinID, extra ...any) {
	args := append([]any{"bin_id", id.String()}, extra...)
	s.logger.InfoContext(ctx, "storage: "+msg, args...)
}
