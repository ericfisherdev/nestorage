package domain

import (
	"context"
	"time"

	"github.com/google/uuid"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// Notification is the notify context's own aggregate root: one event
// delivered to one recipient over one channel. NSTR-44 writes exclusively
// ChannelInApp rows, born StatusSent — an in-app row IS its own delivery,
// with SentAt stamped the instant it commits, never left pending. NSTR-89
// additionally writes/claims ChannelEmail rows through the same table (see
// Outbox's own doc).
type Notification struct {
	ID          NotificationID
	RecipientID identity.UserID
	Channel     Channel
	EventType   EventType
	Title       string
	Body        string
	Status      Status
	// Attempts counts delivery attempts made so far — always 0 for an
	// in-app row (delivered on the first and only attempt, at Enqueue
	// time). RescheduleOrFail increments it for a claimed email row that
	// failed to send.
	Attempts int
	// NextAttemptAt is when the (future) email dispatcher may next claim
	// this row — nil for an in-app row, which is never claimed.
	NextAttemptAt *time.Time
	// SentAt is set the instant an in-app row is enqueued, or by MarkSent
	// once a claimed email row's send succeeds. Nil for a pending or
	// failed row.
	SentAt *time.Time
	// ReadAt is nil until MarkRead sets it — the inbox/unread-badge's own
	// read/unread distinction.
	ReadAt *time.Time
	// SourceType/SourceID optionally name the domain entity that raised
	// this notification (e.g. "return_request" + a
	// storage/domain.ReturnRequestID) — informational only, not a foreign
	// key (see 00014_notification.sql's own doc for why).
	SourceType string
	SourceID   *uuid.UUID
	CreatedAt  time.Time
}

// Enqueuer is the narrow producer port (ISP): a caller that only needs to
// write a notification — Notifier's own fan-out — depends on this, not on
// Outbox's full dispatcher-side lifecycle, mirroring
// storage/domain.ReturnRequestRepository's own narrower ISP siblings.
type Enqueuer interface {
	// Enqueue persists n. NSTR-44 always calls this with Channel=ChannelInApp
	// and Status already StatusSent (SentAt set, Attempts 0) — the caller
	// decides the row's born-delivered state, Enqueue merely persists it.
	// Sets n.CreatedAt on success.
	Enqueue(ctx context.Context, n *Notification) error
}

// Outbox is the (future) email dispatcher's own persistence port — embeds
// Enqueuer and adds the claim/settle lifecycle NSTR-89's dispatcher drives.
// NSTR-44 implements and gated-tests every method here (adapter.postgres.go
// satisfies it in full, per Sprint 6 decision's own "What this ticket still
// owns" list) even though nothing in this ticket calls ClaimDue/MarkSent/
// RescheduleOrFail — CONDITION B keeps the dispatcher itself, the only
// caller, wholly in NSTR-89, so this port ships exercisable and correct
// ahead of a consumer that can adopt it unchanged.
//
// ClaimDue's own claim is optimistic, mirroring Nestova's own
// notify/adapter.OutboxRepository.ClaimDue (reference only, not imported):
// one statement (a FOR UPDATE SKIP LOCKED CTE feeding an UPDATE) atomically
// selects due pending rows and flips them to StatusSent with SentAt still
// nil — that (sent, sent_at IS NULL) shape IS the claim marker, needing no
// fourth status. MarkSent then stamps SentAt to confirm a successful send;
// RescheduleOrFail walks a failed send back from that claimed state,
// atomically incrementing Attempts and either reverting to StatusPending
// with a fresh NextAttemptAt (under the caller-supplied cap) or landing on
// the terminal StatusFailed (at or over it). Backoff duration and the
// attempt cap are the (future) dispatcher's own policy, computed from its
// own config (Sprint 6 decision CONDITION B: no such config exists in this
// ticket) and handed in as plain parameters — this port only ever
// mechanically applies whatever the caller already decided.
type Outbox interface {
	Enqueuer

	// ClaimDue atomically selects up to limit pending notifications whose
	// NextAttemptAt is due (nil or <= now()), transitions them to
	// StatusSent (SentAt left nil — the claim marker), and returns them.
	ClaimDue(ctx context.Context, limit int) ([]*Notification, error)

	// MarkSent confirms a claimed row's delivery: stamps SentAt. Returns
	// ErrNotificationNotFound when id is unknown.
	MarkSent(ctx context.Context, id NotificationID, sentAt time.Time) error

	// RescheduleOrFail walks a claimed row back from a failed send:
	// increments Attempts, and either reverts to StatusPending with
	// NextAttemptAt = nextAttemptAt (Attempts still under maxAttempts) or
	// lands on the terminal StatusFailed (Attempts at or over it). Returns
	// ErrNotificationNotFound when id is unknown.
	RescheduleOrFail(ctx context.Context, id NotificationID, nextAttemptAt time.Time, maxAttempts int) error
}
