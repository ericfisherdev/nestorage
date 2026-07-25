// Package app contains the notify context's application services: Notifier
// (this file), InboxService (inbox.go), and the in-app title/body builders
// (templates.go) — the in-app half NSTR-44 owns. NSTR-89 appends its own
// dispatcher and email builders alongside these, never rewriting them
// (Sprint 6 decision).
package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/notify/domain"
	storageapp "github.com/ericfisherdev/nestorage/internal/storage/app"
)

// sourceTypeReturnRequest names the originating aggregate for every
// notification this ticket writes — both event types NSTR-44 raises are
// about the same return_request row (Sprint 6 reconciliation R10's own
// ReturnRequested->return_requested / ReturnRequestsFulfilled->item_returned
// mapping), so one constant covers both.
const sourceTypeReturnRequest = "return_request"

// Notifier is the implementation behind storage/app.ReturnRequestNotifier —
// the consumer-declared port NSTR-43 declared and wired a no-op for
// (storageapp.NopReturnRequestNotifier), swapped for this at
// composition-root time (cmd/server/main.go) with no change to
// OperationService or ReturnRequestService's own code (R10). Both port
// methods are void — post-commit and best-effort from the caller's
// perspective — so every failure here is logged, never propagated back into
// an already-committed storage operation.
type Notifier struct {
	store  domain.Enqueuer
	prefs  domain.PreferenceReader
	clock  func() time.Time
	logger *slog.Logger
}

// Compile-time assurance Notifier satisfies the port it exists to
// implement — mirrors storage/app's own var _ NopReturnRequestNotifier
// pattern, just checked from the implementing side instead.
var _ storageapp.ReturnRequestNotifier = (*Notifier)(nil)

// NewNotifier constructs Notifier. Every dependency is required; a missing
// one panics at construction time, matching every other constructor in
// this codebase. clock is injected (not time.Now reached for internally)
// so a test can assert an exact SentAt/CreatedAt without racing the wall
// clock — production wiring passes time.Now.
func NewNotifier(store domain.Enqueuer, prefs domain.PreferenceReader, clock func() time.Time, logger *slog.Logger) *Notifier {
	if store == nil {
		panic("notify/app: NewNotifier requires a non-nil domain.Enqueuer")
	}
	if prefs == nil {
		panic("notify/app: NewNotifier requires a non-nil domain.PreferenceReader")
	}
	if clock == nil {
		panic("notify/app: NewNotifier requires a non-nil clock")
	}
	if logger == nil {
		panic("notify/app: NewNotifier requires a non-nil logger")
	}
	return &Notifier{store: store, prefs: prefs, clock: clock, logger: logger}
}

// ReturnRequested implements storage/app.ReturnRequestNotifier: notifies
// n's holder that a return was requested (EventTypeReturnRequested).
func (s *Notifier) ReturnRequested(ctx context.Context, n storageapp.ReturnRequestNotification) {
	sourceID := uuid.UUID(n.RequestID)
	if err := s.notify(ctx, domain.EventTypeReturnRequested, n.HolderID, returnRequestedTitle(n), returnRequestedBody(n), &sourceID); err != nil {
		s.logger.ErrorContext(ctx, "notify: return requested", "error", err, "return_request_id", n.RequestID.String())
	}
}

// ReturnRequestsFulfilled implements storage/app.ReturnRequestNotifier:
// notifies each fulfilled request's own REQUESTER (Sprint 6 reconciliation
// R18 — the recipient rule's original pass reasoned only about holders and
// missed this) that their item is back (EventTypeItemReturned). One
// notify call per request: a single placement change can fulfil several
// open requests on the same item, each with a different requester.
func (s *Notifier) ReturnRequestsFulfilled(ctx context.Context, ns []storageapp.ReturnRequestNotification) {
	for _, n := range ns {
		sourceID := uuid.UUID(n.RequestID)
		if err := s.notify(ctx, domain.EventTypeItemReturned, n.RequesterID, itemReturnedTitle(n), itemReturnedBody(n), &sourceID); err != nil {
			s.logger.ErrorContext(ctx, "notify: item returned", "error", err, "return_request_id", n.RequestID.String())
		}
	}
}

// notify resolves recipientID's channel preferences for eventType and
// writes one Notification row per enabled channel. Returns
// domain.ErrNoRecipient for a zero recipientID (defense in depth — every
// known caller already guarantees a real person; see that sentinel's own
// doc) rather than enqueueing an undeliverable row, and wraps any
// PreferenceReader failure; both are the caller's cue to log and stop,
// never to propagate into the storage operation that triggered this.
func (s *Notifier) notify(ctx context.Context, eventType domain.EventType, recipientID identity.UserID, title, body string, sourceID *uuid.UUID) error {
	if recipientID == (identity.UserID{}) {
		return domain.ErrNoRecipient
	}
	channels, err := s.prefs.ChannelsFor(ctx, recipientID, eventType)
	if err != nil {
		return fmt.Errorf("notify: resolve preferences: %w", err)
	}
	for _, ch := range channels {
		s.enqueueChannel(ctx, ch, eventType, recipientID, title, body, sourceID)
	}
	return nil
}

// enqueueChannel writes one Notification row for a single resolved
// channel. Only ChannelInApp is deliverable by this ticket — a row is born
// StatusSent, SentAt stamped by s.clock, Attempts 0, since an in-app row IS
// its own delivery (Sprint 6 decision CONDITION A). Any other channel
// (today, only ChannelEmail) is a no-op here: this ticket declares no
// EmailSender and writes no pending row for it (CONDITION A's own "nothing
// in this ticket ever writes a pending row"); NSTR-89 extends this switch
// with its own case once the email half lands. The default case is
// intentional, not a placeholder — house style requires every enum switch
// name its unhandled cases explicitly rather than fall through silently.
func (s *Notifier) enqueueChannel(ctx context.Context, ch domain.Channel, eventType domain.EventType, recipientID identity.UserID, title, body string, sourceID *uuid.UUID) {
	switch ch {
	case domain.ChannelInApp:
		now := s.clock()
		notification := &domain.Notification{
			ID: domain.NewNotificationID(), RecipientID: recipientID,
			Channel: domain.ChannelInApp, EventType: eventType,
			Title: title, Body: body,
			Status: domain.StatusSent, Attempts: 0, SentAt: &now,
			SourceType: sourceTypeReturnRequest, SourceID: sourceID,
		}
		if err := s.store.Enqueue(ctx, notification); err != nil {
			s.logger.ErrorContext(ctx, "notify: enqueue in-app notification", "error", err, "event_type", eventType.String())
		}
	default:
		// email (and any future channel) is not deliverable by this build.
	}
}

// StaticPreferenceReader is the fixed default domain.PreferenceReader
// NSTR-44 wires at composition-root time (cmd/server/main.go): every
// recipient/event pair resolves to in-app only, so a fresh install is
// fully offline-safe with zero preference configuration. NSTR-45 swaps in
// its own real, per-user implementation here with no change to Notifier or
// its callers — the same OCP seam storage/app.NopReturnRequestNotifier's
// own doc describes.
type StaticPreferenceReader struct{}

// ChannelsFor always returns [ChannelInApp], matching PreferenceReader's
// own "never empty, in-app always on" contract.
func (StaticPreferenceReader) ChannelsFor(context.Context, identity.UserID, domain.EventType) ([]domain.Channel, error) {
	return []domain.Channel{domain.ChannelInApp}, nil
}
