package app

import (
	"context"
	"fmt"
	"log/slog"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// locationRepository is the narrow port (ISP) LocationService depends on,
// satisfied by domain.LocationRepository (a superset) and by test fakes.
type locationRepository interface {
	Create(ctx context.Context, l *domain.Location) error
	FindVisibleByID(ctx context.Context, viewer identity.Principal, id domain.LocationID) (*domain.Location, error)
	List(ctx context.Context) ([]domain.Location, error)
	Rename(ctx context.Context, id domain.LocationID, name string) error
	Delete(ctx context.Context, id domain.LocationID) error
}

// binLister is the narrow port (ISP) LocationService depends on to compute
// per-location bin counts scoped to what viewer may see, satisfied by
// domain.BinRepository (a superset, via ListVisible) and by test fakes.
// Grouping ListVisible's result by LocationID in memory — rather than a
// dedicated counting query — costs nothing at a household's scale (dozens
// of bins, not millions) and keeps this service from needing a second bin
// query shape.
type binLister interface {
	ListVisible(ctx context.Context, viewer identity.Principal) ([]domain.Bin, error)
}

// LocationSummary pairs a Location with how many bins viewer may see inside
// it — the read model LocationService.List returns for the location index
// cards. A private bin belonging to another member is excluded from
// BinCount exactly as it is from BinService.ListVisible's own result, since
// both are derived from the same viewer-scoped query.
type LocationSummary struct {
	Location domain.Location
	BinCount int
}

// LocationService implements the location browse/manage operations NSTR-31's
// web handlers consume: a bin-count-carrying list, a single visible read,
// and the create/rename/delete commands the location CRUD forms drive.
type LocationService struct {
	locations locationRepository
	bins      binLister
	logger    *slog.Logger
}

// NewLocationService constructs LocationService. Every dependency is
// required; a missing one panics at construction time, matching every other
// constructor in this codebase (see NewItemService).
func NewLocationService(locations locationRepository, bins binLister, logger *slog.Logger) *LocationService {
	if locations == nil {
		panic("storage/app: NewLocationService requires a non-nil locationRepository")
	}
	if bins == nil {
		panic("storage/app: NewLocationService requires a non-nil binLister")
	}
	if logger == nil {
		panic("storage/app: NewLocationService requires a non-nil logger")
	}
	return &LocationService{locations: locations, bins: bins, logger: logger}
}

// List returns every location ordered by name, each carrying how many bins
// viewer may see inside it.
func (s *LocationService) List(ctx context.Context, viewer identity.Principal) ([]LocationSummary, error) {
	locations, err := s.locations.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("app: list locations: %w", err)
	}
	bins, err := s.bins.ListVisible(ctx, viewer)
	if err != nil {
		return nil, fmt.Errorf("app: list locations: %w", err)
	}

	counts := make(map[domain.LocationID]int, len(locations))
	for _, b := range bins {
		counts[b.LocationID]++
	}

	summaries := make([]LocationSummary, 0, len(locations))
	for _, l := range locations {
		summaries = append(summaries, LocationSummary{Location: l, BinCount: counts[l.ID]})
	}
	return summaries, nil
}

// Get returns the location viewer may see. Returns a wrapped
// domain.ErrLocationNotFound when id is unknown.
func (s *LocationService) Get(ctx context.Context, viewer identity.Principal, id domain.LocationID) (*domain.Location, error) {
	l, err := s.locations.FindVisibleByID(ctx, viewer, id)
	if err != nil {
		return nil, fmt.Errorf("app: get location: %w", err)
	}
	return l, nil
}

// Create validates and persists a new location. Returns a wrapped
// domain.ErrInvalidLocationName for a blank/over-long name from
// domain.ValidateLocationName, or a wrapped error from the repository's
// foreign-key checks (an unknown createdBy or parentID).
func (s *LocationService) Create(ctx context.Context, name, description string, parentID *domain.LocationID, createdBy identity.UserID) (*domain.Location, error) {
	validName, err := domain.ValidateLocationName(name)
	if err != nil {
		return nil, err
	}
	l := &domain.Location{
		ID:          domain.NewLocationID(),
		Name:        validName,
		Description: description,
		ParentID:    parentID,
		CreatedBy:   createdBy,
	}
	if err := s.locations.Create(ctx, l); err != nil {
		return nil, fmt.Errorf("app: create location: %w", err)
	}
	s.logAction(ctx, "location created", l.ID)
	return l, nil
}

// Rename validates and overwrites id's name. Returns a wrapped
// domain.ErrInvalidLocationName for a blank/over-long name, or a wrapped
// domain.ErrLocationNotFound when id is unknown.
func (s *LocationService) Rename(ctx context.Context, id domain.LocationID, name string) error {
	validName, err := domain.ValidateLocationName(name)
	if err != nil {
		return err
	}
	if err := s.locations.Rename(ctx, id, validName); err != nil {
		return fmt.Errorf("app: rename location: %w", err)
	}
	s.logAction(ctx, "location renamed", id)
	return nil
}

// Delete removes id. Returns a wrapped domain.ErrLocationNotFound when id is
// unknown, or domain.ErrLocationNotEmpty when a dependent row (a child
// location or a bin) still references it.
func (s *LocationService) Delete(ctx context.Context, id domain.LocationID) error {
	if err := s.locations.Delete(ctx, id); err != nil {
		return fmt.Errorf("app: delete location: %w", err)
	}
	s.logAction(ctx, "location deleted", id)
	return nil
}

// logAction writes one INFO-level audit line for a completed location
// mutation. It logs the location's id, never its name — Nestorage's
// PII-out-of-logs convention (see ItemService.logAction).
func (s *LocationService) logAction(ctx context.Context, msg string, id domain.LocationID, extra ...any) {
	args := append([]any{"location_id", id.String()}, extra...)
	s.logger.InfoContext(ctx, "storage: "+msg, args...)
}
