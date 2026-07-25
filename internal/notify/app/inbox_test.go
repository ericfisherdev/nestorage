package app_test

import (
	"context"
	"errors"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	notifyapp "github.com/ericfisherdev/nestorage/internal/notify/app"
	notifydomain "github.com/ericfisherdev/nestorage/internal/notify/domain"
)

// fakeNotificationStore is a configurable notificationStore fake for
// InboxService's hermetic unit tests.
type fakeNotificationStore struct {
	notifications []notifydomain.Notification
	listErr       error
	unreadCount   int
	unreadErr     error
	markReadErr   error

	listCalls     []identity.UserID
	unreadCalls   []identity.UserID
	markReadCalls []struct {
		recipient identity.UserID
		id        notifydomain.NotificationID
	}
}

func (f *fakeNotificationStore) ListForRecipient(_ context.Context, recipientID identity.UserID, _ int) ([]notifydomain.Notification, error) {
	f.listCalls = append(f.listCalls, recipientID)
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.notifications, nil
}

func (f *fakeNotificationStore) UnreadCount(_ context.Context, recipientID identity.UserID) (int, error) {
	f.unreadCalls = append(f.unreadCalls, recipientID)
	if f.unreadErr != nil {
		return 0, f.unreadErr
	}
	return f.unreadCount, nil
}

func (f *fakeNotificationStore) MarkRead(_ context.Context, recipientID identity.UserID, id notifydomain.NotificationID) error {
	f.markReadCalls = append(f.markReadCalls, struct {
		recipient identity.UserID
		id        notifydomain.NotificationID
	}{recipientID, id})
	return f.markReadErr
}

func TestNewInboxService_PanicsOnNilDeps(t *testing.T) {
	store := &fakeNotificationStore{}
	logger := testLogger()

	tests := []struct {
		name string
		fn   func()
	}{
		{"nil store", func() { notifyapp.NewInboxService(nil, logger) }},
		{"nil logger", func() { notifyapp.NewInboxService(store, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewInboxService(%s) did not panic", tt.name)
				}
			}()
			tt.fn()
		})
	}
}

func TestInboxService_List_DelegatesToStore(t *testing.T) {
	recipient := identity.NewUserID()
	want := []notifydomain.Notification{{ID: notifydomain.NewNotificationID(), RecipientID: recipient}}
	store := &fakeNotificationStore{notifications: want}
	svc := notifyapp.NewInboxService(store, testLogger())

	got, err := svc.List(context.Background(), recipient)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != want[0].ID {
		t.Errorf("List = %+v, want %+v", got, want)
	}
	if len(store.listCalls) != 1 || store.listCalls[0] != recipient {
		t.Errorf("ListForRecipient calls = %v, want exactly [%v]", store.listCalls, recipient)
	}
}

func TestInboxService_List_PropagatesStoreError(t *testing.T) {
	store := &fakeNotificationStore{listErr: errors.New("db down")}
	svc := notifyapp.NewInboxService(store, testLogger())

	if _, err := svc.List(context.Background(), identity.NewUserID()); err == nil {
		t.Error("List returned nil error, want the store's own error")
	}
}

func TestInboxService_UnreadCount_DelegatesToStore(t *testing.T) {
	store := &fakeNotificationStore{unreadCount: 3}
	svc := notifyapp.NewInboxService(store, testLogger())

	got, err := svc.UnreadCount(context.Background(), identity.NewUserID())
	if err != nil {
		t.Fatalf("UnreadCount: %v", err)
	}
	if got != 3 {
		t.Errorf("UnreadCount = %d, want 3", got)
	}
}

func TestInboxService_MarkRead_DelegatesToStore(t *testing.T) {
	store := &fakeNotificationStore{}
	svc := notifyapp.NewInboxService(store, testLogger())

	recipient := identity.NewUserID()
	id := notifydomain.NewNotificationID()
	if err := svc.MarkRead(context.Background(), recipient, id); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if len(store.markReadCalls) != 1 || store.markReadCalls[0].recipient != recipient || store.markReadCalls[0].id != id {
		t.Errorf("MarkRead calls = %+v, want exactly one call with (%v, %v)", store.markReadCalls, recipient, id)
	}
}

func TestInboxService_MarkRead_PropagatesNotFound(t *testing.T) {
	store := &fakeNotificationStore{markReadErr: notifydomain.ErrNotificationNotFound}
	svc := notifyapp.NewInboxService(store, testLogger())

	err := svc.MarkRead(context.Background(), identity.NewUserID(), notifydomain.NewNotificationID())
	if !errors.Is(err, notifydomain.ErrNotificationNotFound) {
		t.Errorf("MarkRead = %v, want ErrNotificationNotFound", err)
	}
}
