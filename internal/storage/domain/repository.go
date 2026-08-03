package domain

import (
	"context"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// LocationRepository is the outbound port for persisting and retrieving
// storage locations, scoped throughout (on every read, rename, and delete) by
// a viewer identity.Principal so household membership is enforced in the
// query itself rather than as an after-the-fact filter — the same approach
// BinRepository/ItemRepository take. Implementations live in the adapter
// package.
//
// Persistence contracts (the caller sets identity and valid fields; the
// store sets timestamps):
//   - Create expects l.ID, a valid l.HouseholdID, a validated l.Name (see
//     ValidateLocationName), l.Description, l.ParentID (nil for a top-level
//     location), and a valid l.CreatedBy set; it populates
//     CreatedAt/UpdatedAt. The caller is responsible for validating the name
//     — the store does not re-validate on write.
//   - Rename's caller has already applied ValidateLocationName to name; the
//     store does not re-validate it either.
//
// Error contracts:
//   - Create returns a wrapped error if created_by is unknown (an
//     identity.member foreign-key violation) or if parent_id is unknown (a
//     location foreign-key violation).
//   - FindVisibleByID, Rename, and Delete return ErrLocationNotFound when id
//     is unknown or belongs to a household other than viewer's — the same
//     "not found" masking ErrBinNotFound's own doc requires, so a member of
//     one household cannot even confirm another household's location exists.
//   - List returns an empty slice, not an error, when viewer's household has
//     no locations.
//   - Delete returns ErrLocationNotEmpty when a dependent row (a child
//     location, or a bin) still references it.
type LocationRepository interface {
	Create(ctx context.Context, l *Location) error
	// FindVisibleByID returns the location, scoped to viewer's household.
	// Location carries no per-viewer privacy field — unlike Bin's
	// Visibility — so every location in viewer's own household is visible to
	// every member of it; this is the principal-scoped seam NSTR-30's
	// app.BinMover needs to validate a move's target. Returns
	// ErrLocationNotFound when id is unknown or belongs to a different
	// household.
	FindVisibleByID(ctx context.Context, viewer identity.Principal, id LocationID) (*Location, error)
	// List returns every location in viewer's household, ordered by name,
	// tie-broken by id for a stable order between rows sharing a name.
	List(ctx context.Context, viewer identity.Principal) ([]Location, error)
	// Rename overwrites id's name with a caller-validated name, scoped to
	// viewer's household.
	Rename(ctx context.Context, viewer identity.Principal, id LocationID, name string) error
	// Delete removes id, scoped to viewer's household.
	Delete(ctx context.Context, viewer identity.Principal, id LocationID) error
}
