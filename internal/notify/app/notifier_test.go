package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	notifyapp "github.com/ericfisherdev/nestorage/internal/notify/app"
	notifydomain "github.com/ericfisherdev/nestorage/internal/notify/domain"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"

	storageapp "github.com/ericfisherdev/nestorage/internal/storage/app"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// fakeEnqueuer is a configurable domain.Enqueuer fake recording every
// Notification handed to it, mirroring the "fake<Port>" naming convention
// storage/app's own test fakes use (e.g. operations_test.go).
type fakeEnqueuer struct {
	calls []*notifydomain.Notification
	err   error
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, n *notifydomain.Notification) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, n)
	return nil
}

// fakePreferenceReader is a configurable domain.PreferenceReader fake.
type fakePreferenceReader struct {
	channels []notifydomain.Channel
	err      error

	calls []notifydomain.EventType
}

func (f *fakePreferenceReader) ChannelsFor(_ context.Context, _ identity.UserID, eventType notifydomain.EventType) ([]notifydomain.Channel, error) {
	f.calls = append(f.calls, eventType)
	if f.err != nil {
		return nil, f.err
	}
	return f.channels, nil
}

func TestNewNotifier_PanicsOnNilDeps(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	prefs := &fakePreferenceReader{channels: []notifydomain.Channel{notifydomain.ChannelInApp}}
	clock := fixedClock(time.Now())
	logger := testLogger()

	tests := []struct {
		name string
		fn   func()
	}{
		{"nil store", func() { notifyapp.NewNotifier(nil, prefs, clock, logger) }},
		{"nil prefs", func() { notifyapp.NewNotifier(enqueuer, nil, clock, logger) }},
		{"nil clock", func() { notifyapp.NewNotifier(enqueuer, prefs, nil, logger) }},
		{"nil logger", func() { notifyapp.NewNotifier(enqueuer, prefs, clock, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewNotifier(%s) did not panic", tt.name)
				}
			}()
			tt.fn()
		})
	}
}

func returnRequestNotification(holderID identity.UserID, requesterLabel, itemName string, message *string) storageapp.ReturnRequestNotification {
	return storageapp.ReturnRequestNotification{
		RequestID: storagedomain.NewReturnRequestID(), ItemID: storagedomain.NewItemID(), ItemName: itemName,
		RequesterID: identity.NewUserID(), RequesterLabel: requesterLabel,
		HolderID: holderID, Message: message, CreatedAt: time.Now(),
	}
}

func TestNotifier_ReturnRequested_WritesDeliveredInAppRow(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	prefs := &fakePreferenceReader{channels: []notifydomain.Channel{notifydomain.ChannelInApp}}
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	notifier := notifyapp.NewNotifier(enqueuer, prefs, fixedClock(now), testLogger())

	holder := identity.NewUserID()
	n := returnRequestNotification(holder, "Alex", "Camping stove", nil)
	notifier.ReturnRequested(context.Background(), n)

	if len(enqueuer.calls) != 1 {
		t.Fatalf("Enqueue called %d times, want 1", len(enqueuer.calls))
	}
	got := enqueuer.calls[0]
	if got.RecipientID != holder {
		t.Errorf("RecipientID = %v, want holder %v", got.RecipientID, holder)
	}
	if got.Channel != notifydomain.ChannelInApp {
		t.Errorf("Channel = %v, want ChannelInApp", got.Channel)
	}
	if got.EventType != notifydomain.EventTypeReturnRequested {
		t.Errorf("EventType = %v, want EventTypeReturnRequested", got.EventType)
	}
	if got.Status != notifydomain.StatusSent {
		t.Errorf("Status = %v, want StatusSent (born delivered)", got.Status)
	}
	if got.Attempts != 0 {
		t.Errorf("Attempts = %d, want 0", got.Attempts)
	}
	if got.SentAt == nil || !got.SentAt.Equal(now) {
		t.Errorf("SentAt = %v, want %v", got.SentAt, now)
	}
	if !strings.Contains(got.Title, "Camping stove") {
		t.Errorf("Title = %q, want it to name the item", got.Title)
	}
	if !strings.Contains(got.Body, "Alex") || !strings.Contains(got.Body, "Camping stove") {
		t.Errorf("Body = %q, want it to name the requester and the item", got.Body)
	}
	if got.SourceType != "return_request" {
		t.Errorf("SourceType = %q, want \"return_request\"", got.SourceType)
	}
	if got.SourceID == nil || got.SourceID.String() != n.RequestID.String() {
		t.Errorf("SourceID = %v, want %v", got.SourceID, n.RequestID)
	}
}

func TestNotifier_ReturnRequested_MessageIncludedInBody(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	prefs := &fakePreferenceReader{channels: []notifydomain.Channel{notifydomain.ChannelInApp}}
	notifier := notifyapp.NewNotifier(enqueuer, prefs, fixedClock(time.Now()), testLogger())

	message := "need it back by Friday"
	n := returnRequestNotification(identity.NewUserID(), "Alex", "Camping stove", &message)
	notifier.ReturnRequested(context.Background(), n)

	if len(enqueuer.calls) != 1 {
		t.Fatalf("Enqueue called %d times, want 1", len(enqueuer.calls))
	}
	if !strings.Contains(enqueuer.calls[0].Body, message) {
		t.Errorf("Body = %q, want it to include the requester's message %q", enqueuer.calls[0].Body, message)
	}
}

func TestNotifier_ReturnRequested_ZeroHolder_NoEnqueue(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	prefs := &fakePreferenceReader{channels: []notifydomain.Channel{notifydomain.ChannelInApp}}
	notifier := notifyapp.NewNotifier(enqueuer, prefs, fixedClock(time.Now()), testLogger())

	n := returnRequestNotification(identity.UserID{}, "Alex", "Camping stove", nil)
	notifier.ReturnRequested(context.Background(), n)

	if len(enqueuer.calls) != 0 {
		t.Errorf("Enqueue called %d times for a zero recipient, want 0", len(enqueuer.calls))
	}
	if len(prefs.calls) != 0 {
		t.Errorf("PreferenceReader consulted %d times for a zero recipient, want 0 — ErrNoRecipient must short-circuit before preferences are read", len(prefs.calls))
	}
}

func TestNotifier_ReturnRequested_PreferenceReaderError_NoEnqueueNoPanic(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	prefs := &fakePreferenceReader{err: errors.New("boom")}
	notifier := notifyapp.NewNotifier(enqueuer, prefs, fixedClock(time.Now()), testLogger())

	n := returnRequestNotification(identity.NewUserID(), "Alex", "Camping stove", nil)
	notifier.ReturnRequested(context.Background(), n)

	if len(enqueuer.calls) != 0 {
		t.Errorf("Enqueue called %d times after a PreferenceReader error, want 0", len(enqueuer.calls))
	}
}

func TestNotifier_ReturnRequested_EnqueueError_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ReturnRequested panicked on an Enqueue failure: %v", r)
		}
	}()
	enqueuer := &fakeEnqueuer{err: errors.New("db down")}
	prefs := &fakePreferenceReader{channels: []notifydomain.Channel{notifydomain.ChannelInApp}}
	notifier := notifyapp.NewNotifier(enqueuer, prefs, fixedClock(time.Now()), testLogger())

	n := returnRequestNotification(identity.NewUserID(), "Alex", "Camping stove", nil)
	// Must not panic — a storage failure here is logged, never propagated
	// into the already-committed return-request operation.
	notifier.ReturnRequested(context.Background(), n)
}

func TestNotifier_ReturnRequested_EmailOnlyPreference_SkippedNotEnqueued(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	prefs := &fakePreferenceReader{channels: []notifydomain.Channel{notifydomain.ChannelEmail}}
	notifier := notifyapp.NewNotifier(enqueuer, prefs, fixedClock(time.Now()), testLogger())

	n := returnRequestNotification(identity.NewUserID(), "Alex", "Camping stove", nil)
	notifier.ReturnRequested(context.Background(), n)

	if len(enqueuer.calls) != 0 {
		t.Errorf("Enqueue called %d times for an email-only preference, want 0 — NSTR-44 delivers no email and writes no pending row", len(enqueuer.calls))
	}
}

func TestNotifier_ReturnRequestsFulfilled_OneNotificationPerRequester(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	prefs := &fakePreferenceReader{channels: []notifydomain.Channel{notifydomain.ChannelInApp}}
	notifier := notifyapp.NewNotifier(enqueuer, prefs, fixedClock(time.Now()), testLogger())

	first := returnRequestNotification(identity.NewUserID(), "Alex", "Camping stove", nil)
	second := returnRequestNotification(identity.NewUserID(), "Sam", "Camping stove", nil)
	notifier.ReturnRequestsFulfilled(context.Background(), []storageapp.ReturnRequestNotification{first, second})

	if len(enqueuer.calls) != 2 {
		t.Fatalf("Enqueue called %d times, want 2 (one per requester)", len(enqueuer.calls))
	}
	gotRecipients := map[identity.UserID]bool{enqueuer.calls[0].RecipientID: true, enqueuer.calls[1].RecipientID: true}
	if !gotRecipients[first.RequesterID] || !gotRecipients[second.RequesterID] {
		t.Errorf("recipients = %v, want both requesters %v and %v", gotRecipients, first.RequesterID, second.RequesterID)
	}
	for _, got := range enqueuer.calls {
		if got.EventType != notifydomain.EventTypeItemReturned {
			t.Errorf("EventType = %v, want EventTypeItemReturned", got.EventType)
		}
	}
}

func TestNotifier_ReturnRequestsFulfilled_Empty_NoEnqueue(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	prefs := &fakePreferenceReader{channels: []notifydomain.Channel{notifydomain.ChannelInApp}}
	notifier := notifyapp.NewNotifier(enqueuer, prefs, fixedClock(time.Now()), testLogger())

	notifier.ReturnRequestsFulfilled(context.Background(), nil)

	if len(enqueuer.calls) != 0 {
		t.Errorf("Enqueue called %d times for an empty fulfilment list, want 0", len(enqueuer.calls))
	}
}

func TestStaticPreferenceReader_AlwaysInAppOnly(t *testing.T) {
	reader := notifyapp.StaticPreferenceReader{}
	channels, err := reader.ChannelsFor(context.Background(), identity.NewUserID(), notifydomain.EventTypeReturnRequested)
	if err != nil {
		t.Fatalf("ChannelsFor: %v", err)
	}
	if len(channels) != 1 || channels[0] != notifydomain.ChannelInApp {
		t.Errorf("ChannelsFor = %v, want exactly [ChannelInApp]", channels)
	}
}
