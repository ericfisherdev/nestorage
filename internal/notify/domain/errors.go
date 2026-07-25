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
)
