package domain

import (
	"context"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// PreferenceRepository is the outbound port (DIP) app.PreferenceService
// depends on for persistence. Rows are sparse: no row for a given
// user/event/channel means the caller-supplied default applies, not an
// error — ListForUser and Get both return an empty slice, never an error,
// when nothing is stored.
type PreferenceRepository interface {
	// ListForUser returns every stored row for userID, across every event
	// type and channel — the settings screen's own read, which needs the
	// full matrix in one call. Empty, not an error, for a user with no
	// stored preferences.
	ListForUser(ctx context.Context, userID identity.UserID) ([]Preference, error)
	// Get returns userID's stored rows for one event type only —
	// ChannelsFor's own read, scoped to the single event type it was
	// asked about rather than loading the user's full matrix on every
	// notification. Empty, not an error, when nothing is stored for that
	// event type.
	Get(ctx context.Context, userID identity.UserID, eventType EventType) ([]Preference, error)
	// Upsert persists p, replacing any existing row for the same
	// (UserID, EventType, Channel) key — the settings screen's own write,
	// always called with Channel == ChannelEmail (see Preference's own
	// doc for why ChannelInApp never reaches this method).
	Upsert(ctx context.Context, p Preference) error
}
