package adapter_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/notify/adapter"
	"github.com/ericfisherdev/nestorage/internal/notify/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
)

// notifyFixture bundles an isolated pool and its own NotificationRepository
// for the gated tests below — dbtest.Harness.NewIsolatedPool must be called
// exactly once per test (mirrors storage/adapter's own itemFixture doc).
type notifyFixture struct {
	pool *pgxpool.Pool
	repo *adapter.NotificationRepository
}

func newNotifyFixture(t *testing.T) *notifyFixture {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "notify")
	return &notifyFixture{pool: pool, repo: adapter.NewNotificationRepository(pool)}
}

// seedUser inserts a minimal identity.household + identity.member + profile
// row directly (rather than depending on the identity bounded context's own
// repository, which this package has no reason to import) and returns the
// member's id — every notification in this suite needs a real recipient_id
// to satisfy notification_recipient_id_fkey.
func (f *notifyFixture) seedUser(t *testing.T) identity.UserID {
	t.Helper()
	id := identity.NewUserID()
	householdID := identity.NewHouseholdID()
	email := id.String() + "@example.test"

	const householdQ = `INSERT INTO identity.household (id, name) VALUES ($1, 'Test Household')`
	if _, err := f.pool.Exec(testCtx(t), householdQ, householdID.String()); err != nil {
		t.Fatalf("seed household: %v", err)
	}
	const memberQ = `INSERT INTO identity.member (id, household_id, display_name, email, password_hash, role) VALUES ($1, $2, 'Test User', $3, 'x', 'adult')`
	if _, err := f.pool.Exec(testCtx(t), memberQ, id.String(), householdID.String(), email); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	const profileQ = `INSERT INTO profile (member_id, color) VALUES ($1, 'indigo')`
	if _, err := f.pool.Exec(testCtx(t), profileQ, id.String()); err != nil {
		t.Fatalf("seed user profile: %v", err)
	}
	return id
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

func newInAppNotification(recipientID identity.UserID, sentAt time.Time) *domain.Notification {
	return &domain.Notification{
		ID: domain.NewNotificationID(), RecipientID: recipientID,
		Channel: domain.ChannelInApp, EventType: domain.EventTypeReturnRequested,
		Title: "Return requested: Camping stove", Body: "Alex asked for it back.",
		Status: domain.StatusSent, Attempts: 0, SentAt: &sentAt,
		SourceType: "return_request",
	}
}

func newPendingEmailNotification(recipientID identity.UserID, nextAttemptAt *time.Time) *domain.Notification {
	return &domain.Notification{
		ID: domain.NewNotificationID(), RecipientID: recipientID,
		Channel: domain.ChannelEmail, EventType: domain.EventTypeItemReturned,
		Title: "Camping stove is back", Body: "It was returned and is ready to check out again.",
		Status: domain.StatusPending, Attempts: 0, NextAttemptAt: nextAttemptAt,
		SourceType: "return_request",
	}
}

func TestNewNotificationRepository_NilExecutorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewNotificationRepository(nil) did not panic")
		}
	}()
	adapter.NewNotificationRepository(nil)
}

func TestNotificationRepository_Enqueue_RoundTrip(t *testing.T) {
	f := newNotifyFixture(t)
	recipient := f.seedUser(t)
	sentAt := time.Now().UTC().Truncate(time.Microsecond)
	n := newInAppNotification(recipient, sentAt)

	if err := f.repo.Enqueue(testCtx(t), n); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if n.CreatedAt.IsZero() {
		t.Error("Enqueue left CreatedAt zero")
	}

	got, err := f.repo.ListForRecipient(testCtx(t), recipient, 10)
	if err != nil {
		t.Fatalf("ListForRecipient: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListForRecipient returned %d rows, want 1", len(got))
	}
	row := got[0]
	if row.ID != n.ID || row.RecipientID != recipient || row.Channel != domain.ChannelInApp ||
		row.EventType != domain.EventTypeReturnRequested || row.Title != n.Title || row.Body != n.Body {
		t.Errorf("round-tripped row = %+v, want it to match the enqueued notification %+v", row, n)
	}
	if row.Status != domain.StatusSent || row.Attempts != 0 {
		t.Errorf("round-tripped row status/attempts = %v/%d, want StatusSent/0 (born delivered)", row.Status, row.Attempts)
	}
	if row.SentAt == nil || !row.SentAt.Equal(sentAt) {
		t.Errorf("round-tripped SentAt = %v, want %v", row.SentAt, sentAt)
	}
	if row.ReadAt != nil {
		t.Errorf("round-tripped ReadAt = %v, want nil (unread)", row.ReadAt)
	}
	if row.SourceType != "return_request" {
		t.Errorf("round-tripped SourceType = %q, want \"return_request\"", row.SourceType)
	}
}

func TestNotificationRepository_Enqueue_UnknownRecipient_Errors(t *testing.T) {
	f := newNotifyFixture(t)
	n := newInAppNotification(identity.NewUserID(), time.Now())

	if err := f.repo.Enqueue(testCtx(t), n); err == nil {
		t.Error("Enqueue with an unknown recipient_id returned nil error, want the foreign-key violation surfaced")
	}
}

func TestNotificationRepository_ListForRecipient_NewestFirst(t *testing.T) {
	f := newNotifyFixture(t)
	recipient := f.seedUser(t)

	first := newInAppNotification(recipient, time.Now())
	if err := f.repo.Enqueue(testCtx(t), first); err != nil {
		t.Fatalf("Enqueue(first): %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	second := newInAppNotification(recipient, time.Now())
	if err := f.repo.Enqueue(testCtx(t), second); err != nil {
		t.Fatalf("Enqueue(second): %v", err)
	}

	got, err := f.repo.ListForRecipient(testCtx(t), recipient, 10)
	if err != nil {
		t.Fatalf("ListForRecipient: %v", err)
	}
	if len(got) != 2 || got[0].ID != second.ID || got[1].ID != first.ID {
		t.Errorf("ListForRecipient order = %+v, want [second, first] (newest first)", got)
	}
}

func TestNotificationRepository_ListForRecipient_RespectsLimit(t *testing.T) {
	f := newNotifyFixture(t)
	recipient := f.seedUser(t)
	for range 3 {
		if err := f.repo.Enqueue(testCtx(t), newInAppNotification(recipient, time.Now())); err != nil {
			t.Fatalf("seed enqueue: %v", err)
		}
	}

	got, err := f.repo.ListForRecipient(testCtx(t), recipient, 2)
	if err != nil {
		t.Fatalf("ListForRecipient: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("ListForRecipient(limit=2) returned %d rows, want 2", len(got))
	}
}

func TestNotificationRepository_ListForRecipient_Empty(t *testing.T) {
	f := newNotifyFixture(t)
	got, err := f.repo.ListForRecipient(testCtx(t), identity.NewUserID(), 10)
	if err != nil {
		t.Fatalf("ListForRecipient: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListForRecipient(no rows) = %+v, want empty slice", got)
	}
}

func TestNotificationRepository_UnreadCount(t *testing.T) {
	f := newNotifyFixture(t)
	recipient := f.seedUser(t)

	if count, err := f.repo.UnreadCount(testCtx(t), recipient); err != nil || count != 0 {
		t.Fatalf("UnreadCount(no rows) = (%d, %v), want (0, nil)", count, err)
	}

	first := newInAppNotification(recipient, time.Now())
	second := newInAppNotification(recipient, time.Now())
	for _, n := range []*domain.Notification{first, second} {
		if err := f.repo.Enqueue(testCtx(t), n); err != nil {
			t.Fatalf("seed enqueue: %v", err)
		}
	}

	if count, err := f.repo.UnreadCount(testCtx(t), recipient); err != nil || count != 2 {
		t.Fatalf("UnreadCount(2 unread) = (%d, %v), want (2, nil)", count, err)
	}

	if err := f.repo.MarkRead(testCtx(t), recipient, first.ID); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if count, err := f.repo.UnreadCount(testCtx(t), recipient); err != nil || count != 1 {
		t.Errorf("UnreadCount(1 unread after MarkRead) = (%d, %v), want (1, nil)", count, err)
	}
}

func TestNotificationRepository_MarkRead_Idempotent(t *testing.T) {
	f := newNotifyFixture(t)
	recipient := f.seedUser(t)
	n := newInAppNotification(recipient, time.Now())
	if err := f.repo.Enqueue(testCtx(t), n); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := f.repo.MarkRead(testCtx(t), recipient, n.ID); err != nil {
		t.Fatalf("MarkRead(first): %v", err)
	}
	got, err := f.repo.ListForRecipient(testCtx(t), recipient, 10)
	if err != nil || len(got) != 1 || got[0].ReadAt == nil {
		t.Fatalf("after first MarkRead, ListForRecipient = %+v, %v, want one row with ReadAt set", got, err)
	}
	firstReadAt := *got[0].ReadAt

	// A second MarkRead on an already-read row must succeed as a no-op,
	// not error, and must not move ReadAt.
	if err := f.repo.MarkRead(testCtx(t), recipient, n.ID); err != nil {
		t.Fatalf("MarkRead(second, already read): %v", err)
	}
	got, err = f.repo.ListForRecipient(testCtx(t), recipient, 10)
	if err != nil || len(got) != 1 || got[0].ReadAt == nil || !got[0].ReadAt.Equal(firstReadAt) {
		t.Errorf("after second MarkRead, ReadAt = %v, want unchanged %v", got, firstReadAt)
	}
}

func TestNotificationRepository_MarkRead_UnknownNotFound(t *testing.T) {
	f := newNotifyFixture(t)
	recipient := f.seedUser(t)

	err := f.repo.MarkRead(testCtx(t), recipient, domain.NewNotificationID())
	if !errors.Is(err, domain.ErrNotificationNotFound) {
		t.Errorf("MarkRead(unknown id) = %v, want ErrNotificationNotFound", err)
	}
}

// TestNotificationRepository_MarkRead_WrongRecipientMasked proves ownership
// is enforced in the SQL itself: a notification belonging to a different
// recipient is indistinguishable from an unknown one, mirroring
// ReturnRequestRepository.Cancel's identical masking.
func TestNotificationRepository_MarkRead_WrongRecipientMasked(t *testing.T) {
	f := newNotifyFixture(t)
	owner := f.seedUser(t)
	stranger := f.seedUser(t)
	n := newInAppNotification(owner, time.Now())
	if err := f.repo.Enqueue(testCtx(t), n); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	err := f.repo.MarkRead(testCtx(t), stranger, n.ID)
	if !errors.Is(err, domain.ErrNotificationNotFound) {
		t.Errorf("MarkRead(wrong recipient) = %v, want ErrNotificationNotFound", err)
	}

	got, err := f.repo.ListForRecipient(testCtx(t), owner, 10)
	if err != nil || len(got) != 1 || got[0].ReadAt != nil {
		t.Error("a rejected MarkRead by a stranger must leave the notification unread")
	}
}

func TestNotificationRepository_ClaimDue_ClaimsOnlyPendingAndDue(t *testing.T) {
	f := newNotifyFixture(t)
	recipient := f.seedUser(t)

	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Hour)
	due := newPendingEmailNotification(recipient, &past)
	notYetDue := newPendingEmailNotification(recipient, &future)
	alreadySent := newInAppNotification(recipient, time.Now())
	for _, n := range []*domain.Notification{due, notYetDue, alreadySent} {
		if err := f.repo.Enqueue(testCtx(t), n); err != nil {
			t.Fatalf("seed enqueue: %v", err)
		}
	}

	claimed, err := f.repo.ClaimDue(testCtx(t), 10)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != due.ID {
		t.Fatalf("ClaimDue = %+v, want exactly the one due pending row %v", claimed, due.ID)
	}
	if claimed[0].Status != domain.StatusSent || claimed[0].SentAt != nil {
		t.Errorf("claimed row status/SentAt = %v/%v, want StatusSent with SentAt still nil (the claim marker)", claimed[0].Status, claimed[0].SentAt)
	}

	// A second claim must not re-claim the same row (SKIP LOCKED's own
	// exclusion, proven here via the row's now-changed status rather than
	// true concurrency).
	again, err := f.repo.ClaimDue(testCtx(t), 10)
	if err != nil {
		t.Fatalf("ClaimDue (second call): %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second ClaimDue = %+v, want empty (already claimed)", again)
	}
}

func TestNotificationRepository_ClaimDue_NilNextAttemptAtIsDue(t *testing.T) {
	f := newNotifyFixture(t)
	recipient := f.seedUser(t)
	n := newPendingEmailNotification(recipient, nil)
	if err := f.repo.Enqueue(testCtx(t), n); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	claimed, err := f.repo.ClaimDue(testCtx(t), 10)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != n.ID {
		t.Errorf("ClaimDue = %+v, want the row with a nil NextAttemptAt claimed as due immediately", claimed)
	}
}

func TestNotificationRepository_ClaimDue_Empty(t *testing.T) {
	f := newNotifyFixture(t)
	claimed, err := f.repo.ClaimDue(testCtx(t), 10)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(claimed) != 0 {
		t.Errorf("ClaimDue(no pending rows) = %+v, want empty slice", claimed)
	}
}

func TestNotificationRepository_MarkSent_StampsSentAt(t *testing.T) {
	f := newNotifyFixture(t)
	recipient := f.seedUser(t)
	past := time.Now().Add(-time.Minute)
	n := newPendingEmailNotification(recipient, &past)
	if err := f.repo.Enqueue(testCtx(t), n); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	claimed, err := f.repo.ClaimDue(testCtx(t), 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimDue: %+v, %v", claimed, err)
	}

	sentAt := time.Now().UTC().Truncate(time.Microsecond)
	if err := f.repo.MarkSent(testCtx(t), n.ID, sentAt); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}

	got, err := f.repo.ListForRecipient(testCtx(t), recipient, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListForRecipient: %+v, %v", got, err)
	}
	if got[0].Status != domain.StatusSent || got[0].SentAt == nil || !got[0].SentAt.Equal(sentAt) {
		t.Errorf("after MarkSent, row = %+v, want StatusSent with SentAt %v", got[0], sentAt)
	}
}

func TestNotificationRepository_MarkSent_UnknownNotFound(t *testing.T) {
	f := newNotifyFixture(t)
	err := f.repo.MarkSent(testCtx(t), domain.NewNotificationID(), time.Now())
	if !errors.Is(err, domain.ErrNotificationNotFound) {
		t.Errorf("MarkSent(unknown id) = %v, want ErrNotificationNotFound", err)
	}
}

func TestNotificationRepository_RescheduleOrFail_UnderCap_RevertsToPending(t *testing.T) {
	f := newNotifyFixture(t)
	recipient := f.seedUser(t)
	past := time.Now().Add(-time.Minute)
	n := newPendingEmailNotification(recipient, &past)
	if err := f.repo.Enqueue(testCtx(t), n); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := f.repo.ClaimDue(testCtx(t), 10); err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}

	next := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Microsecond)
	if err := f.repo.RescheduleOrFail(testCtx(t), n.ID, next, 5); err != nil {
		t.Fatalf("RescheduleOrFail: %v", err)
	}

	got, err := f.repo.ListForRecipient(testCtx(t), recipient, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListForRecipient: %+v, %v", got, err)
	}
	row := got[0]
	if row.Status != domain.StatusPending {
		t.Errorf("Status = %v, want StatusPending (under the attempt cap)", row.Status)
	}
	if row.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", row.Attempts)
	}
	if row.NextAttemptAt == nil || !row.NextAttemptAt.Equal(next) {
		t.Errorf("NextAttemptAt = %v, want %v", row.NextAttemptAt, next)
	}
	if row.SentAt != nil {
		t.Errorf("SentAt = %v, want nil (claim undone)", row.SentAt)
	}
}

func TestNotificationRepository_RescheduleOrFail_AtCap_Fails(t *testing.T) {
	f := newNotifyFixture(t)
	recipient := f.seedUser(t)
	past := time.Now().Add(-time.Minute)
	n := newPendingEmailNotification(recipient, &past)
	if err := f.repo.Enqueue(testCtx(t), n); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := f.repo.ClaimDue(testCtx(t), 10); err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}

	// maxAttempts=1: the first RescheduleOrFail already brings attempts to
	// 1, which is >= the cap, so this must land on StatusFailed rather than
	// rescheduling.
	if err := f.repo.RescheduleOrFail(testCtx(t), n.ID, time.Now().Add(time.Minute), 1); err != nil {
		t.Fatalf("RescheduleOrFail: %v", err)
	}

	got, err := f.repo.ListForRecipient(testCtx(t), recipient, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListForRecipient: %+v, %v", got, err)
	}
	row := got[0]
	if row.Status != domain.StatusFailed {
		t.Errorf("Status = %v, want StatusFailed (attempt cap reached)", row.Status)
	}
	if row.NextAttemptAt != nil {
		t.Errorf("NextAttemptAt = %v, want nil for a terminally failed row", row.NextAttemptAt)
	}
}

func TestNotificationRepository_RescheduleOrFail_UnknownNotFound(t *testing.T) {
	f := newNotifyFixture(t)
	err := f.repo.RescheduleOrFail(testCtx(t), domain.NewNotificationID(), time.Now(), 5)
	if !errors.Is(err, domain.ErrNotificationNotFound) {
		t.Errorf("RescheduleOrFail(unknown id) = %v, want ErrNotificationNotFound", err)
	}
}
