package app_test

// dispatcher_gated_test.go covers NSTR-89's own end-to-end drain against
// real rows (NESTORAGE_TEST_DATABASE_URL): enqueue a pending email row
// through the real adapter.NotificationRepository (the same Outbox NSTR-44
// already gated-tests ClaimDue/MarkSent/RescheduleOrFail against), run one
// Dispatcher.DispatchOnce, and assert it lands StatusSent. NSTR-44 already
// owns the gated coverage of ClaimDue's skip-locked disjointness and the
// two settle methods as adapter methods in their own right — this file
// does not duplicate that, only proves Dispatcher drives them correctly
// against a real database.
//
// suffix "notify_dispatch" is deliberately distinct from adapter's own
// "notify" suffix (postgres_gated_test.go, preference_postgres_gated_test.go):
// dbtest.Harness.NewIsolatedPool derives one database PER suffix, and Go
// runs different packages' test binaries concurrently, so this package
// (app_test) sharing the adapter package's "notify" suffix would race the
// exact schema-reset collision the harness exists to prevent.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	notifyadapter "github.com/ericfisherdev/nestorage/internal/notify/adapter"
	notifyapp "github.com/ericfisherdev/nestorage/internal/notify/app"
	notifydomain "github.com/ericfisherdev/nestorage/internal/notify/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
)

// seedDispatcherUser inserts a minimal app_user row directly, mirroring
// adapter's own notifyFixture.seedUser — this package has no reason to
// import the identity bounded context's own repository just to seed a
// fixture row.
func seedDispatcherUser(t *testing.T, pool *pgxpool.Pool) identity.UserID {
	t.Helper()
	id := identity.NewUserID()
	const q = `INSERT INTO app_user (id, display_name, email, password_hash, role, color) VALUES ($1, 'Test User', $2, 'x', 'member', 'indigo')`
	email := id.String() + "@example.test"
	if _, err := pool.Exec(context.Background(), q, id.String(), email); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

// gatedFakeResolver is a trivial domain.EmailAddressResolver returning a
// fixed address for any id — the real cross-context resolution (identity's
// UserRepository.FindByID structurally satisfying this port) is proven by
// cmd/server's own composition, not by this gated test, which is scoped to
// the Outbox round trip.
type gatedFakeResolver struct{ email string }

func (r gatedFakeResolver) FindByID(_ context.Context, id identity.UserID) (*identity.User, error) {
	return &identity.User{ID: id, Email: r.email}, nil
}

// gatedFakeSender is a domain.EmailSender that always succeeds, recording
// every message it was asked to send.
type gatedFakeSender struct{ calls []notifydomain.EmailMessage }

func (s *gatedFakeSender) Send(_ context.Context, msg notifydomain.EmailMessage) error {
	s.calls = append(s.calls, msg)
	return nil
}

func TestDispatcher_DispatchOnce_DrainsRealPendingEmailRow(t *testing.T) {
	pool := dbtest.Harness.NewIsolatedPool(t, "notify_dispatch")
	outbox := notifyadapter.NewNotificationRepository(pool)
	recipient := seedDispatcherUser(t, pool)

	past := time.Now().Add(-time.Minute)
	pending := &notifydomain.Notification{
		ID: notifydomain.NewNotificationID(), RecipientID: recipient,
		Channel: notifydomain.ChannelEmail, EventType: notifydomain.EventTypeItemReturned,
		Title: "Camping stove is back", Body: "It was returned and is ready to check out again.",
		Status: notifydomain.StatusPending, Attempts: 0, NextAttemptAt: &past,
		SourceType: "return_request",
	}
	if err := outbox.Enqueue(context.Background(), pending); err != nil {
		t.Fatalf("seed enqueue: %v", err)
	}

	sender := &gatedFakeSender{}
	resolver := gatedFakeResolver{email: "holder@example.test"}
	d := notifyapp.NewDispatcher(outbox, resolver, sender, time.Now, time.Minute, 10, 5, testLogger())

	d.DispatchOnce(context.Background())

	if len(sender.calls) != 1 {
		t.Fatalf("Send called %d times, want 1", len(sender.calls))
	}
	if sender.calls[0].To != "holder@example.test" {
		t.Errorf("Send To = %q, want %q", sender.calls[0].To, "holder@example.test")
	}
	if sender.calls[0].Subject != pending.Title {
		t.Errorf("Send Subject = %q, want %q", sender.calls[0].Subject, pending.Title)
	}

	got, err := outbox.ListForRecipient(context.Background(), recipient, 10)
	if err != nil {
		t.Fatalf("ListForRecipient: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListForRecipient = %+v, want exactly the one seeded row", got)
	}
	if got[0].Status != notifydomain.StatusSent {
		t.Errorf("Status = %v, want StatusSent after DispatchOnce drains it", got[0].Status)
	}
	if got[0].SentAt == nil {
		t.Error("SentAt is nil, want it stamped by MarkSent")
	}
}

func TestDispatcher_DispatchOnce_NoDueRows_NoOp(t *testing.T) {
	pool := dbtest.Harness.NewIsolatedPool(t, "notify_dispatch")
	outbox := notifyadapter.NewNotificationRepository(pool)

	sender := &gatedFakeSender{}
	resolver := gatedFakeResolver{email: "holder@example.test"}
	d := notifyapp.NewDispatcher(outbox, resolver, sender, time.Now, time.Minute, 10, 5, testLogger())

	d.DispatchOnce(context.Background())

	if len(sender.calls) != 0 {
		t.Errorf("Send called %d times with no pending rows, want 0", len(sender.calls))
	}
}
