package domain

import (
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// Preference is one stored row: whether userID has opted a single channel
// in or out for a single event type. NSTR-45 only ever writes rows with
// Channel == ChannelEmail — ChannelInApp is structurally always-on and is
// never stored (see PreferenceReader's own doc) — but the type itself
// stays generic over Channel so a future push channel needs no shape
// change here, only a new value written through it.
type Preference struct {
	UserID    identity.UserID
	EventType EventType
	Channel   Channel
	Enabled   bool
}

// EffectiveChannels merges stored (a user's own rows for one event type,
// as returned by PreferenceRepository.Get) with the fixed default and
// returns the channels a notification for that event type should actually
// be delivered over. ChannelInApp is unconditionally included — no stored
// row can add, remove, or disable it, so the result is never empty and
// never silent by construction (Sprint 6 reconciliation R15). ChannelEmail
// is included only when stored carries an enabled=true row for it; any
// other stored row (including one explicitly disabling it) is absent from
// the result, since absence and "explicitly off" are both "do not
// deliver" outcomes here.
func EffectiveChannels(stored []Preference) []Channel {
	channels := []Channel{ChannelInApp}
	for _, p := range stored {
		if p.Channel == ChannelEmail && p.Enabled {
			channels = append(channels, ChannelEmail)
		}
	}
	return channels
}
