package domain

import "fmt"

// Channel is a notification's delivery mechanism. Stored as text, validated
// here — the domain mirror of notification_channel_check.
//
// Canonical spelling (Sprint 6 reconciliation: the ticket's own
// markdown-to-ADF conversion had eaten these identifiers' underscores as
// emphasis, repaired and restated in a code block immune to that
// conversion): in_app, email.
type Channel string

// The two known delivery channels. ChannelEmail is declared here even
// though NSTR-44 delivers nothing over it — NSTR-89 needs the value to
// exist on this type, not a second one, so a PreferenceReader written
// against NSTR-44's own enum compiles unchanged once NSTR-89 lands.
const (
	// ChannelInApp is the only channel NSTR-44 delivers over: a synchronous
	// local Postgres insert plus a server-rendered inbox, with no WAN
	// involvement anywhere — the channel that makes notifications
	// offline-safe by construction. Always enabled, never user-disableable
	// (see PreferenceReader's own doc).
	ChannelInApp Channel = "in_app"
	// ChannelEmail is opt-in and WAN-dependent (NSTR-89, out of scope
	// here). This ticket's own PreferenceReader stub never returns it.
	ChannelEmail Channel = "email"
)

// Valid reports whether c is a known channel.
func (c Channel) Valid() bool {
	switch c {
	case ChannelInApp, ChannelEmail:
		return true
	default:
		return false
	}
}

// String returns the channel's stored value.
func (c Channel) String() string { return string(c) }

// ParseChannel validates and returns a Channel, or a wrapped
// ErrInvalidChannel naming the offending value.
func ParseChannel(s string) (Channel, error) {
	c := Channel(s)
	if !c.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidChannel, s)
	}
	return c, nil
}

// Status is a notification row's own delivery lifecycle state — distinct
// from domain.ReturnRequestStatus, which tracks the return request that
// may have raised this notification, a different aggregate in a different
// bounded context. Stored as text, validated here — the domain mirror of
// notification_status_check.
type Status string

// The three known notification statuses. NSTR-44 only ever writes
// StatusSent — an in-app row is born delivered at Enqueue time, never
// pending (see notification.go's own Outbox doc). StatusPending and
// StatusFailed exist for NSTR-89's email rows, exercised here only through
// the Outbox port's own gated tests.
const (
	// StatusPending marks a row not yet delivered — the (future) email
	// dispatcher's own claim target.
	StatusPending Status = "pending"
	// StatusSent marks a row successfully delivered: for an in_app row,
	// stamped at Enqueue time; for an email row, stamped by MarkSent once
	// the (future) dispatcher's send succeeds.
	StatusSent Status = "sent"
	// StatusFailed marks a row whose delivery exhausted its retry budget —
	// RescheduleOrFail's own terminal outcome once attempts reaches the
	// caller-supplied cap.
	StatusFailed Status = "failed"
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusSent, StatusFailed:
		return true
	default:
		return false
	}
}

// String returns the status's stored value.
func (s Status) String() string { return string(s) }

// ParseStatus validates and returns a Status, or a wrapped ErrInvalidStatus
// naming the offending value.
func ParseStatus(s string) (Status, error) {
	st := Status(s)
	if !st.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidStatus, s)
	}
	return st, nil
}

// EventType identifies WHY a notification was raised — the semantic kind of
// event, distinct from both Channel (HOW it is delivered) and the Go method
// names on storage/app.ReturnRequestNotifier that raise it (Sprint 6
// reconciliation R10: ReturnRequested/ReturnRequestsFulfilled are a
// different, unrelated pair of identifiers that happen to describe the same
// two moments). A recipient's per-event-type channel preference
// (PreferenceReader.ChannelsFor) is keyed on this value.
//
// Canonical spelling (see Channel's own doc for why this is restated in a
// code block): return_requested, item_returned.
type EventType string

// The two known event types, both raised by storage/app.ReturnRequestNotifier:
// ReturnRequested maps to EventTypeReturnRequested,
// ReturnRequestsFulfilled maps to EventTypeItemReturned (R10).
const (
	// EventTypeReturnRequested fires when a household member asks for a
	// checked-out item back — the holder is the recipient.
	EventTypeReturnRequested EventType = "return_requested"
	// EventTypeItemReturned fires when a placement change fulfils one or
	// more open return requests on an item — each request's own requester
	// is a recipient (Sprint 6 reconciliation R18).
	EventTypeItemReturned EventType = "item_returned"
)

// Valid reports whether e is a known event type.
func (e EventType) Valid() bool {
	switch e {
	case EventTypeReturnRequested, EventTypeItemReturned:
		return true
	default:
		return false
	}
}

// String returns the event type's stored value.
func (e EventType) String() string { return string(e) }

// ParseEventType validates and returns an EventType, or a wrapped
// ErrInvalidEventType naming the offending value.
func ParseEventType(s string) (EventType, error) {
	e := EventType(s)
	if !e.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidEventType, s)
	}
	return e, nil
}
