package domain

import "errors"

// Domain errors for the notify bounded context. NSTR-45 appends its own
// preference-related sentinels to this same file rather than starting a
// second errors.go — one context, one error file (Sprint 6 reconciliation
// R11), matching storage/domain.errors.go's identical convention.
var (
	// ErrNoRecipient is returned by Notifier when asked to notify a zero
	// identity.UserID. Defense in depth, not a path any known caller can
	// currently reach: the account api key's integration principal is
	// never a recipient, because app.ReturnRequestService.Request already
	// rejects a non-identity.KindUser actor before a return request can
	// even be raised (storage/domain.ErrHolderRequired), and
	// OperationService.transition's own fulfilment fan-out only ever
	// notifies existing requesters, who are real people for the identical
	// reason (Sprint 6 reconciliation R18).
	ErrNoRecipient = errors.New("notify: notification has no recipient")

	// ErrNotificationNotFound is returned by NotificationRepository methods
	// that look up or mutate a specific notification (MarkSent,
	// RescheduleOrFail, MarkRead) when no matching row exists — including
	// when a row exists but does not belong to the requesting recipient
	// (MarkRead masks the two identically, the same "not found" masking
	// storage/domain.ErrReturnRequestNotFound's own doc requires for a
	// wrong-requester Cancel).
	ErrNotificationNotFound = errors.New("notify: notification not found")

	// ErrInvalidChannel is returned (wrapped, with the offending value) by
	// ParseChannel for a string that is not one of the two known Channel
	// values — the domain mirror of notification_channel_check.
	ErrInvalidChannel = errors.New("notify: invalid channel")

	// ErrInvalidStatus is returned (wrapped, with the offending value) by
	// ParseStatus for a string that is not one of the three known Status
	// values — the domain mirror of notification_status_check.
	ErrInvalidStatus = errors.New("notify: invalid status")

	// ErrInvalidEventType is returned (wrapped, with the offending value)
	// by ParseEventType for a string that is not one of the two known
	// EventType values — the domain mirror of notification_event_type_check.
	ErrInvalidEventType = errors.New("notify: invalid event type")

	// ErrChannelNotConfigurable is returned by the one internal path that
	// could otherwise be asked to toggle ChannelInApp. NSTR-45 appends
	// this and the two sentinels below to NSTR-44's own errors.go rather
	// than starting a second one (see this file's own doc). It is not a
	// runtime invariant guarding against a caller mistake so much as
	// documentation: PreferenceService.SetEmailEnabled's own signature
	// has no channel parameter at all, so there is no live call path that
	// can actually reach this — the "cannot disable every channel"
	// acceptance criterion is met by that construction, not by this
	// check (Sprint 6 reconciliation R12).
	ErrChannelNotConfigurable = errors.New("notify: channel is not user-configurable")

	// ErrInvalidChannel and ErrInvalidEventType above already cover the
	// two enum-parse failures a preference could name; no separate
	// ErrPreferenceNotFound exists because PreferenceRepository's own doc
	// requires ListForUser/Get to answer an empty slice, never an error,
	// for a user with nothing stored.

	// ErrEmailNotConfigured is returned by EmailSender.Send when the
	// sender cannot deliver at all (NSTR-89) — e.g. the SES sender
	// constructed with an incomplete configuration. Terminal: Dispatcher
	// marks the row StatusFailed immediately rather than spending any of
	// its retry budget on a send that can never succeed until an operator
	// fixes the configuration and redeploys.
	ErrEmailNotConfigured = errors.New("notify: email sender not configured")
)
