package domain

import (
	"context"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// LocationRepository is the outbound port for persisting and retrieving
// storage locations. Implementations live in the adapter package.
//
// Persistence contracts (the caller sets identity and valid fields; the
// store sets timestamps):
//   - Create expects l.ID, a validated l.Name (see ValidateLocationName),
//     l.Description, l.ParentID (nil for a top-level location), and a valid
//     l.CreatedBy set; it populates CreatedAt/UpdatedAt. The caller is
//     responsible for validating the name — the store does not re-validate
//     on write.
//   - Rename and the caller both apply to a name that has already passed
//     ValidateLocationName; the store does not re-validate it either.
//
// Error contracts:
//   - Create returns a wrapped error if created_by is unknown (an app_user
//     foreign-key violation) or if parent_id is unknown (a location
//     foreign-key violation).
//   - FindByID and FindVisibleByID return ErrLocationNotFound when id is
//     unknown.
//   - List returns an empty slice, not an error, when no locations exist.
//   - Rename returns ErrLocationNotFound when id is unknown.
//   - Delete returns ErrLocationNotFound when id is unknown, or
//     ErrLocationNotEmpty when a dependent row (a child location today; a
//     bin once NSTR-27 lands) still references it.
type LocationRepository interface {
	Create(ctx context.Context, l *Location) error
	FindByID(ctx context.Context, id LocationID) (*Location, error)
	// FindVisibleByID returns the location, scoped to what viewer may see.
	// Location carries no per-viewer privacy field — unlike Bin's
	// Visibility — so every location is visible to every principal today;
	// this is the principal-scoped seam NSTR-30's app.BinMover needs to
	// validate a move's target (and a later ticket's private-location
	// feature would tighten) rather than a second, ad hoc "not found"
	// contract. Returns ErrLocationNotFound when id is unknown.
	FindVisibleByID(ctx context.Context, viewer identity.Principal, id LocationID) (*Location, error)
	// List returns every location ordered by name, tie-broken by id for a
	// stable order between rows sharing a name.
	List(ctx context.Context) ([]Location, error)
	// Rename overwrites id's name with a caller-validated name.
	Rename(ctx context.Context, id LocationID, name string) error
	Delete(ctx context.Context, id LocationID) error
}
