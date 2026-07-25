package app

// Internal (white-box) test file, unlike the rest of this package's tests:
// dispatchOnce/deliver/backoffDuration are unexported, and driving them
// directly here avoids a flaky sleep-and-poll test against the real ticker
// in Run (see the TIMEZONE/flakiness warning in this sprint's own history —
// tests must never race a real clock). TestDispatcher_Run_StopsOnContextCancel
// below is the one exception, bounded by a hard timeout rather than a bare
// sleep.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/notify/domain"
)

// fakeOutbox is a configurable domain.Outbox fake recording every call,
// mirroring notifier_test.go's fakeEnqueuer naming convention.
type fakeOutbox struct {
	claimDue    []*domain.Notification
	claimDueErr error

	markSentIDs []domain.NotificationID
	markSentErr error

	rescheduleCalls []rescheduleCall
	rescheduleErr   error
}

type rescheduleCall struct {
	id            domain.NotificationID
	nextAttemptAt time.Time
	maxAttempts   int
}

func (f *fakeOutbox) Enqueue(_ context.Context, _ *domain.Notification) error {
	return errors.New("fakeOutbox: Enqueue is not used by Dispatcher and should not be called")
}

func (f *fakeOutbox) ClaimDue(_ context.Context, _ int) ([]*domain.Notification, error) {
	if f.claimDueErr != nil {
		return nil, f.claimDueErr
	}
	return f.claimDue, nil
}

func (f *fakeOutbox) MarkSent(_ context.Context, id domain.NotificationID, _ time.Time) error {
	f.markSentIDs = append(f.markSentIDs, id)
	return f.markSentErr
}

func (f *fakeOutbox) RescheduleOrFail(_ context.Context, id domain.NotificationID, nextAttemptAt time.Time, maxAttempts int) error {
	f.rescheduleCalls = append(f.rescheduleCalls, rescheduleCall{id: id, nextAttemptAt: nextAttemptAt, maxAttempts: maxAttempts})
	return f.rescheduleErr
}

// fakeResolver is a configurable domain.EmailAddressResolver fake.
type fakeResolver struct {
	email string
	err   error
}

func (f *fakeResolver) FindByID(_ context.Context, id identity.UserID) (*identity.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &identity.User{ID: id, Email: f.email}, nil
}

// fakeSender is a configurable domain.EmailSender fake.
type fakeSender struct {
	err   error
	calls []domain.EmailMessage
}

func (f *fakeSender) Send(_ context.Context, msg domain.EmailMessage) error {
	f.calls = append(f.calls, msg)
	return f.err
}

func dispatcherTestLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// fixedClock is this file's own copy of notifier_test.go's identical
// helper: package app (white-box, for dispatchOnce/backoffDuration access)
// cannot see package app_test's declarations.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func pendingEmailNotification() *domain.Notification {
	return &domain.Notification{
		ID: domain.NewNotificationID(), RecipientID: identity.NewUserID(),
		Channel: domain.ChannelEmail, EventType: domain.EventTypeItemReturned,
		Title: "Camping stove is back", Body: "It was returned and is ready to check out again.",
		Status: domain.StatusPending, Attempts: 0,
	}
}

func TestNewDispatcher_PanicsOnNilOrInvalidDeps(t *testing.T) {
	outbox := &fakeOutbox{}
	resolver := &fakeResolver{email: "a@example.test"}
	sender := &fakeSender{}
	clock := fixedClock(time.Now())
	logger := dispatcherTestLogger()

	tests := []struct {
		name string
		fn   func()
	}{
		{"nil outbox", func() { NewDispatcher(nil, resolver, sender, clock, time.Second, 10, 5, logger) }},
		{"nil resolver", func() { NewDispatcher(outbox, nil, sender, clock, time.Second, 10, 5, logger) }},
		{"nil sender", func() { NewDispatcher(outbox, resolver, nil, clock, time.Second, 10, 5, logger) }},
		{"nil clock", func() { NewDispatcher(outbox, resolver, sender, nil, time.Second, 10, 5, logger) }},
		{"zero interval", func() { NewDispatcher(outbox, resolver, sender, clock, 0, 10, 5, logger) }},
		{"negative interval", func() { NewDispatcher(outbox, resolver, sender, clock, -time.Second, 10, 5, logger) }},
		{"zero batchSize", func() { NewDispatcher(outbox, resolver, sender, clock, time.Second, 0, 5, logger) }},
		{"zero maxAttempts", func() { NewDispatcher(outbox, resolver, sender, clock, time.Second, 10, 0, logger) }},
		{"nil logger", func() { NewDispatcher(outbox, resolver, sender, clock, time.Second, 10, 5, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewDispatcher(%s) did not panic", tt.name)
				}
			}()
			tt.fn()
		})
	}
}

func TestDispatcher_DispatchOnce_SuccessfulSend_MarksSent(t *testing.T) {
	n := pendingEmailNotification()
	outbox := &fakeOutbox{claimDue: []*domain.Notification{n}}
	resolver := &fakeResolver{email: "holder@example.test"}
	sender := &fakeSender{}
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	d := NewDispatcher(outbox, resolver, sender, fixedClock(now), time.Minute, 10, 5, dispatcherTestLogger())

	d.DispatchOnce(context.Background())

	if len(sender.calls) != 1 {
		t.Fatalf("Send called %d times, want 1", len(sender.calls))
	}
	msg := sender.calls[0]
	if msg.To != "holder@example.test" {
		t.Errorf("To = %q, want %q", msg.To, "holder@example.test")
	}
	if msg.Subject != n.Title {
		t.Errorf("Subject = %q, want n.Title %q", msg.Subject, n.Title)
	}
	if msg.TextBody != n.Body {
		t.Errorf("TextBody = %q, want n.Body %q", msg.TextBody, n.Body)
	}
	if msg.HTMLBody == "" {
		t.Error("HTMLBody is empty, want a minimal HTML rendering of the body")
	}
	if len(outbox.markSentIDs) != 1 || outbox.markSentIDs[0] != n.ID {
		t.Errorf("MarkSent calls = %v, want exactly [%v]", outbox.markSentIDs, n.ID)
	}
	if len(outbox.rescheduleCalls) != 0 {
		t.Errorf("RescheduleOrFail called %d times on a success, want 0", len(outbox.rescheduleCalls))
	}
}

func TestDispatcher_DispatchOnce_SendFails_ReschedulesWithBackoff(t *testing.T) {
	n := pendingEmailNotification()
	n.Attempts = 2
	outbox := &fakeOutbox{claimDue: []*domain.Notification{n}}
	resolver := &fakeResolver{email: "holder@example.test"}
	sender := &fakeSender{err: errors.New("smtp: connection refused")}
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	d := NewDispatcher(outbox, resolver, sender, fixedClock(now), time.Minute, 10, 5, dispatcherTestLogger())

	d.DispatchOnce(context.Background())

	if len(outbox.markSentIDs) != 0 {
		t.Errorf("MarkSent called %d times on a failure, want 0", len(outbox.markSentIDs))
	}
	if len(outbox.rescheduleCalls) != 1 {
		t.Fatalf("RescheduleOrFail called %d times, want 1", len(outbox.rescheduleCalls))
	}
	call := outbox.rescheduleCalls[0]
	if call.id != n.ID {
		t.Errorf("rescheduled id = %v, want %v", call.id, n.ID)
	}
	if call.maxAttempts != 5 {
		t.Errorf("maxAttempts passed = %d, want the dispatcher's own 5", call.maxAttempts)
	}
	wantNext := now.Add(backoffDuration(2))
	if !call.nextAttemptAt.Equal(wantNext) {
		t.Errorf("nextAttemptAt = %v, want %v (backoff for attempts=2)", call.nextAttemptAt, wantNext)
	}
}

func TestDispatcher_DispatchOnce_ErrEmailNotConfigured_ForceFailsWithoutSpendingRetryBudget(t *testing.T) {
	n := pendingEmailNotification()
	outbox := &fakeOutbox{claimDue: []*domain.Notification{n}}
	resolver := &fakeResolver{email: "holder@example.test"}
	sender := &fakeSender{err: domain.ErrEmailNotConfigured}
	d := NewDispatcher(outbox, resolver, sender, fixedClock(time.Now()), time.Minute, 10, 5, dispatcherTestLogger())

	d.DispatchOnce(context.Background())

	if len(outbox.rescheduleCalls) != 1 {
		t.Fatalf("RescheduleOrFail called %d times, want 1", len(outbox.rescheduleCalls))
	}
	if outbox.rescheduleCalls[0].maxAttempts != 0 {
		t.Errorf("maxAttempts passed = %d, want 0 (forces StatusFailed immediately, no retry spent)", outbox.rescheduleCalls[0].maxAttempts)
	}
}

func TestDispatcher_DispatchOnce_UnresolvableRecipient_ForceFailsWithoutSendAttempt(t *testing.T) {
	n := pendingEmailNotification()
	outbox := &fakeOutbox{claimDue: []*domain.Notification{n}}
	resolver := &fakeResolver{err: errors.New("identity: user not found")}
	sender := &fakeSender{}
	d := NewDispatcher(outbox, resolver, sender, fixedClock(time.Now()), time.Minute, 10, 5, dispatcherTestLogger())

	d.DispatchOnce(context.Background())

	if len(sender.calls) != 0 {
		t.Errorf("Send called %d times for an unresolvable recipient, want 0", len(sender.calls))
	}
	if len(outbox.rescheduleCalls) != 1 || outbox.rescheduleCalls[0].maxAttempts != 0 {
		t.Errorf("RescheduleOrFail calls = %+v, want exactly one forced fail (maxAttempts=0)", outbox.rescheduleCalls)
	}
}

func TestDispatcher_DispatchOnce_ClaimError_NoPanicNoSend(t *testing.T) {
	outbox := &fakeOutbox{claimDueErr: errors.New("db unavailable")}
	resolver := &fakeResolver{email: "a@example.test"}
	sender := &fakeSender{}
	d := NewDispatcher(outbox, resolver, sender, fixedClock(time.Now()), time.Minute, 10, 5, dispatcherTestLogger())

	d.DispatchOnce(context.Background())

	if len(sender.calls) != 0 {
		t.Errorf("Send called %d times after a claim error, want 0", len(sender.calls))
	}
}

// TestDispatcher_DispatchOnce_OneFailureDoesNotPoisonBatch proves a failing
// row does not stop the loop from reaching its siblings (Sprint 6/NSTR-89
// AC: "A failing row must not poison its batch").
func TestDispatcher_DispatchOnce_OneFailureDoesNotPoisonBatch(t *testing.T) {
	failing := pendingEmailNotification()
	succeeding := pendingEmailNotification()
	outbox := &fakeOutbox{claimDue: []*domain.Notification{failing, succeeding}}
	resolver := &fakeResolver{email: "holder@example.test"}
	// The fake sender fails only for the first message it sees.
	callCount := 0
	sender := sendFuncSender(func(_ context.Context, _ domain.EmailMessage) error {
		callCount++
		if callCount == 1 {
			return errors.New("transient failure")
		}
		return nil
	})
	d := NewDispatcher(outbox, resolver, sender, fixedClock(time.Now()), time.Minute, 10, 5, dispatcherTestLogger())

	d.DispatchOnce(context.Background())

	if callCount != 2 {
		t.Fatalf("Send called %d times, want 2 (both rows attempted)", callCount)
	}
	if len(outbox.rescheduleCalls) != 1 || outbox.rescheduleCalls[0].id != failing.ID {
		t.Errorf("RescheduleOrFail calls = %+v, want exactly one for the failing row %v", outbox.rescheduleCalls, failing.ID)
	}
	if len(outbox.markSentIDs) != 1 || outbox.markSentIDs[0] != succeeding.ID {
		t.Errorf("MarkSent calls = %v, want exactly one for the succeeding row %v", outbox.markSentIDs, succeeding.ID)
	}
}

// sendFuncSender adapts a plain func to domain.EmailSender, for tests that
// need per-call behavior a static fakeSender{err: ...} cannot express.
type sendFuncSender func(ctx context.Context, msg domain.EmailMessage) error

func (f sendFuncSender) Send(ctx context.Context, msg domain.EmailMessage) error { return f(ctx, msg) }

func TestDispatcher_Run_StopsOnContextCancel(t *testing.T) {
	outbox := &fakeOutbox{}
	resolver := &fakeResolver{email: "a@example.test"}
	sender := &fakeSender{}
	d := NewDispatcher(outbox, resolver, sender, fixedClock(time.Now()), time.Millisecond, 10, 5, dispatcherTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of context cancellation")
	}
}

func TestBackoffDuration(t *testing.T) {
	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: -1, want: dispatcherBackoffBase},
		{attempts: 0, want: dispatcherBackoffBase},
		{attempts: 1, want: 2 * dispatcherBackoffBase},
		{attempts: 2, want: 4 * dispatcherBackoffBase},
		{attempts: 5, want: 32 * dispatcherBackoffBase},
		{attempts: dispatcherBackoffMaxShift + 1, want: dispatcherBackoffMax},
		{attempts: 1000, want: dispatcherBackoffMax},
	}
	for _, tt := range tests {
		got := backoffDuration(tt.attempts)
		want := tt.want
		if want > dispatcherBackoffMax {
			want = dispatcherBackoffMax
		}
		if got != want {
			t.Errorf("backoffDuration(%d) = %v, want %v", tt.attempts, got, want)
		}
	}
}
