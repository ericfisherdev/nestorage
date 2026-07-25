package domain

import (
	"context"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// PreferenceReader is the consumer-declared port (ISP) Notifier depends on
// to resolve which channels a recipient wants a given event type delivered
// over, consulted at ENQUEUE time — never by the (future) dispatcher, which
// only ever acts on rows Notifier already decided to write (Sprint 6
// reconciliation R12, restated binding). This ticket declares the port
// only: NSTR-45 implements it for real (a per-user, per-event-type
// preference table) and NSTR-44 ships a static default (see
// app.StaticPreferenceReader) so a fresh install is in-app-only until
// NSTR-45 lands.
//
// ChannelsFor never returns an empty slice: ChannelInApp is structurally
// always on and not user-disableable — the property that makes a
// notification offline-safe by construction rather than by configuration
// (Sprint 6 reconciliation R15). A channel the recipient has disabled is
// simply absent from the result; Notifier treats that as success, not a
// delivery failure, for that channel.
type PreferenceReader interface {
	ChannelsFor(ctx context.Context, userID identity.UserID, eventType EventType) ([]Channel, error)
}
