package domain

import (
	"context"
	"errors"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// ErrPreferenceNotFound is returned by SizePreferenceRepository.Get when
// userID has never printed a label — a sparse-by-default condition, not an
// error a caller should surface: the print screen falls back to the
// registry's own default size (Registry.List's first, stable entry) rather
// than failing the page load.
var ErrPreferenceNotFound = errors.New("labels: no stored size preference")

// SizePreferenceRepository is the outbound port (DIP) NSTR-51's web
// handlers depend on for per-user label size persistence. Deliberately
// Postgres-backed rather than session- or cookie-scoped: a session expires
// on its own hard lifetime, and the entryway kiosk's cookie jar is shared
// across every household member who stands in front of it, so either would
// leak one member's remembered size to the next. Implementations live in
// the adapter package.
type SizePreferenceRepository interface {
	// Get returns userID's remembered size id, or ErrPreferenceNotFound when
	// userID has never printed a label — callers fall back to the registry
	// default in that case, never treating it as a failed page load.
	Get(ctx context.Context, userID identity.UserID) (LabelSizeID, error)
	// Upsert persists sizeID as userID's remembered size, replacing any
	// prior value via a single insert-on-conflict-do-update. Called only
	// after a successful print — the moment the choice is proven real —
	// never from a mere preview.
	Upsert(ctx context.Context, userID identity.UserID, sizeID LabelSizeID) error
}
