package adapter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ericfisherdev/nestcore/db"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/notify/domain"
)

// notificationColumns lists notification's own columns in the exact order
// scanNotification expects — shared by every read query and RETURNING
// clause below, so all stay in lockstep with the scan function, mirroring
// storage/adapter's own returnRequestReturningColumns convention.
const notificationColumns = `id, recipient_id, channel, event_type, title, body, status, attempts, next_attempt_at, sent_at, read_at, source_type, source_id, created_at`

// scanner abstracts pgx.Row and pgx.Rows for the shared scan helper —
// unexported to this package, mirroring storage/adapter's identical type
// (neither can be reused directly across bounded-context packages).
type scanner interface {
	Scan(dest ...any) error
}

// NotificationRepository is the pgx-backed implementation of domain.Enqueuer,
// domain.Outbox, and the narrow inbox-query port app.InboxService depends
// on (notificationStore) — one concrete type satisfying all three, mirroring
// storage/adapter.ItemEventRepository's own dual role. UUIDs are passed and
// scanned as text, matching every other adapter in this codebase, so no pgx
// UUID codec registration is required.
type NotificationRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies both domain-declared ports.
var (
	_ domain.Enqueuer = (*NotificationRepository)(nil)
	_ domain.Outbox   = (*NotificationRepository)(nil)
)

// NewNotificationRepository constructs the repository with an injected
// query executor. NSTR-44 always binds this to the shared pool (Enqueue and
// the inbox reads are never part of another aggregate's transaction — every
// call site fires post-commit, best-effort); db.TX is accepted rather than
// *pgxpool.Pool directly only to match every other repository constructor's
// own signature in this codebase.
func NewNotificationRepository(dbtx db.TX) *NotificationRepository {
	if dbtx == nil {
		panic("notify/adapter: NewNotificationRepository requires a non-nil db.TX")
	}
	return &NotificationRepository{dbtx: dbtx}
}

// Enqueue inserts n and populates n.CreatedAt. The caller (Notifier) has
// already decided n's born state — Channel, Status, Attempts, SentAt are
// persisted exactly as supplied, never defaulted or recomputed here.
func (r *NotificationRepository) Enqueue(ctx context.Context, n *domain.Notification) error {
	if n == nil {
		return errors.New("notify/adapter: enqueue: nil notification")
	}
	const q = `
		INSERT INTO notification
		    (id, recipient_id, channel, event_type, title, body, status, attempts, next_attempt_at, sent_at, source_type, source_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING created_at`
	err := r.dbtx.QueryRow(ctx, q,
		n.ID.String(), n.RecipientID.String(), n.Channel.String(), n.EventType.String(),
		n.Title, n.Body, n.Status.String(), n.Attempts, n.NextAttemptAt, n.SentAt,
		nullableString(n.SourceType), uuidParam(n.SourceID),
	).Scan(&n.CreatedAt)
	if err != nil {
		return fmt.Errorf("enqueue notification: %w", err)
	}
	return nil
}

// ClaimDue atomically claims up to limit pending, due notifications — see
// domain.Outbox's own doc for the single-statement optimistic-claim shape
// (a FOR UPDATE SKIP LOCKED CTE feeding an UPDATE) and why it needs no
// separate "claimed" status.
func (r *NotificationRepository) ClaimDue(ctx context.Context, limit int) ([]*domain.Notification, error) {
	const q = `
		WITH claimed AS (
			SELECT id
			  FROM notification
			 WHERE status = 'pending'
			   AND (next_attempt_at IS NULL OR next_attempt_at <= now())
			 ORDER BY next_attempt_at NULLS FIRST
			 LIMIT $1
			   FOR UPDATE SKIP LOCKED
		)
		UPDATE notification n
		   SET status = 'sent'
		  FROM claimed
		 WHERE n.id = claimed.id
		RETURNING n.` + notificationColumns
	// n.<cols>: only "id" needs the n. qualifier — claimed carries no other
	// column, so every other name below is already unambiguous — but the
	// whole list is prefixed as one string for simplicity, matching the
	// leading column's own required qualification.

	rows, err := r.dbtx.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("claim due notifications: %w", err)
	}
	defer rows.Close()

	notifications := make([]*domain.Notification, 0)
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, fmt.Errorf("claim due notifications: scan: %w", err)
		}
		notifications = append(notifications, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim due notifications: %w", err)
	}
	return notifications, nil
}

// MarkSent stamps sentAt on a claimed row. Returns
// domain.ErrNotificationNotFound when id is unknown.
func (r *NotificationRepository) MarkSent(ctx context.Context, id domain.NotificationID, sentAt time.Time) error {
	const q = `UPDATE notification SET sent_at = $2 WHERE id = $1`
	tag, err := r.dbtx.Exec(ctx, q, id.String(), sentAt)
	if err != nil {
		return fmt.Errorf("mark notification sent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotificationNotFound
	}
	return nil
}

// RescheduleOrFail walks a claimed row back from a failed send: attempts is
// incremented atomically in the same statement that decides the outcome, so
// there is no separate read-then-write race between the check and the
// update. Returns domain.ErrNotificationNotFound when id is unknown.
func (r *NotificationRepository) RescheduleOrFail(ctx context.Context, id domain.NotificationID, nextAttemptAt time.Time, maxAttempts int) error {
	// $2 is cast explicitly: left bare, the CASE branch pairing it against a
	// literal NULL gives Postgres nothing to resolve the parameter's type
	// from, and the whole expression's inferred type (not timestamptz) then
	// fails to assign into next_attempt_at — verified against Postgres 17.
	const q = `
		UPDATE notification
		SET attempts        = attempts + 1,
		    status          = CASE WHEN attempts + 1 >= $3 THEN 'failed' ELSE 'pending' END,
		    next_attempt_at = CASE WHEN attempts + 1 >= $3 THEN NULL ELSE $2::timestamptz END,
		    sent_at         = NULL
		WHERE id = $1`
	tag, err := r.dbtx.Exec(ctx, q, id.String(), nextAttemptAt, maxAttempts)
	if err != nil {
		return fmt.Errorf("reschedule or fail notification: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotificationNotFound
	}
	return nil
}

// ListForRecipient returns recipientID's own notifications, newest first,
// bounded to limit — app.InboxService's own read, backed by
// notification_recipient_read_idx.
func (r *NotificationRepository) ListForRecipient(ctx context.Context, recipientID identity.UserID, limit int) ([]domain.Notification, error) {
	q := `SELECT ` + notificationColumns + ` FROM notification WHERE recipient_id = $1 ORDER BY created_at DESC LIMIT $2`
	rows, err := r.dbtx.Query(ctx, q, recipientID.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()

	notifications := make([]domain.Notification, 0)
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, fmt.Errorf("list notifications: scan: %w", err)
		}
		notifications = append(notifications, *n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	return notifications, nil
}

// UnreadCount returns recipientID's own unread notification count — the
// sidebar badge's own value, backed by the same
// notification_recipient_read_idx.
func (r *NotificationRepository) UnreadCount(ctx context.Context, recipientID identity.UserID) (int, error) {
	const q = `SELECT count(*) FROM notification WHERE recipient_id = $1 AND read_at IS NULL`
	var count int
	if err := r.dbtx.QueryRow(ctx, q, recipientID.String()).Scan(&count); err != nil {
		return 0, fmt.Errorf("count unread notifications: %w", err)
	}
	return count, nil
}

// MarkRead marks id read on recipientID's own behalf — the WHERE clause's
// own id AND recipient_id pairing enforces ownership in the SQL itself
// (mirroring ReturnRequestRepository.Cancel's identical "ownership
// enforced in the SQL" convention), so a notification belonging to a
// different recipient reads as domain.ErrNotificationNotFound, not a
// separate "forbidden" outcome. COALESCE(read_at, now()) makes a repeat
// mark-read of an already-read row a harmless no-op rather than an error.
func (r *NotificationRepository) MarkRead(ctx context.Context, recipientID identity.UserID, id domain.NotificationID) error {
	const q = `UPDATE notification SET read_at = COALESCE(read_at, now()) WHERE id = $1 AND recipient_id = $2`
	tag, err := r.dbtx.Exec(ctx, q, id.String(), recipientID.String())
	if err != nil {
		return fmt.Errorf("mark notification read: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotificationNotFound
	}
	return nil
}

// nullableString returns nil for an empty string, a pointer to s otherwise —
// SourceType is optional (nullable in the schema); an empty Go string must
// not be stored as an empty-string row value.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// uuidParam returns id's string form, or nil for a nil id — SourceID is
// optional (nullable in the schema).
func uuidParam(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

func scanNotification(r scanner) (*domain.Notification, error) {
	var (
		n                                               domain.Notification
		idStr, recipientIDStr, channelStr, eventTypeStr string
		statusStr                                       string
		sourceType, sourceIDStr                         *string
	)
	if err := r.Scan(
		&idStr, &recipientIDStr, &channelStr, &eventTypeStr, &n.Title, &n.Body,
		&statusStr, &n.Attempts, &n.NextAttemptAt, &n.SentAt, &n.ReadAt,
		&sourceType, &sourceIDStr, &n.CreatedAt,
	); err != nil {
		return nil, err
	}

	id, err := domain.ParseNotificationID(idStr)
	if err != nil {
		return nil, fmt.Errorf("id: %w", err)
	}
	recipientID, err := identity.ParseUserID(recipientIDStr)
	if err != nil {
		return nil, fmt.Errorf("recipient id: %w", err)
	}
	channel, err := domain.ParseChannel(channelStr)
	if err != nil {
		return nil, fmt.Errorf("channel: %w", err)
	}
	eventType, err := domain.ParseEventType(eventTypeStr)
	if err != nil {
		return nil, fmt.Errorf("event type: %w", err)
	}
	status, err := domain.ParseStatus(statusStr)
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}

	n.ID, n.RecipientID, n.Channel, n.EventType, n.Status = id, recipientID, channel, eventType, status
	if sourceType != nil {
		n.SourceType = *sourceType
	}
	if sourceIDStr != nil {
		sourceID, err := uuid.Parse(*sourceIDStr)
		if err != nil {
			return nil, fmt.Errorf("source id: %w", err)
		}
		n.SourceID = &sourceID
	}
	return &n, nil
}
