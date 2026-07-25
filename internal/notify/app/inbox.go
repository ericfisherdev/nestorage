package app

import (
	"context"
	"log/slog"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/notify/domain"
)

// inboxListLimit bounds InboxService.List's page size — the notify context
// has no click-to-load pagination (unlike storage's own item history):
// a household's notification volume is low enough that one bounded page
// covers the inbox screen, mirroring how a fixed limit is a reasonable
// simplification elsewhere in this codebase (e.g. shellData.Stats' own
// undercount-rather-than-leak framing).
const inboxListLimit = 50

// notificationStore is the narrow port (ISP) InboxService depends on,
// satisfied by *adapter.NotificationRepository (a superset, alongside
// domain.Outbox — one concrete repository type structurally satisfies
// both, mirroring storage/adapter.ItemEventRepository's own dual role as
// itemEventLister and its own repository interface).
type notificationStore interface {
	ListForRecipient(ctx context.Context, recipientID identity.UserID, limit int) ([]domain.Notification, error)
	UnreadCount(ctx context.Context, recipientID identity.UserID) (int, error)
	MarkRead(ctx context.Context, recipientID identity.UserID, id domain.NotificationID) error
}

// InboxService backs the /notifications screen and the sidebar's unread
// badge: list a recipient's own notifications, their current unread count,
// and mark one read. Every method is scoped to the recipientID the caller
// supplies — InboxWebHandlers always passes the signed-in viewer's own id,
// so one household member's inbox is never reachable by another (MarkRead's
// own ErrNotificationNotFound masking enforces this at the query itself,
// not just by convention here).
type InboxService struct {
	store  notificationStore
	logger *slog.Logger
}

// NewInboxService constructs InboxService. Both dependencies are required;
// a missing one panics at construction time, matching every other
// constructor in this codebase.
func NewInboxService(store notificationStore, logger *slog.Logger) *InboxService {
	if store == nil {
		panic("notify/app: NewInboxService requires a non-nil notificationStore")
	}
	if logger == nil {
		panic("notify/app: NewInboxService requires a non-nil logger")
	}
	return &InboxService{store: store, logger: logger}
}

// List returns recipientID's own notifications, newest first, bounded to
// inboxListLimit.
func (s *InboxService) List(ctx context.Context, recipientID identity.UserID) ([]domain.Notification, error) {
	return s.store.ListForRecipient(ctx, recipientID, inboxListLimit)
}

// UnreadCount returns recipientID's own unread notification count — the
// sidebar badge's own value.
func (s *InboxService) UnreadCount(ctx context.Context, recipientID identity.UserID) (int, error) {
	return s.store.UnreadCount(ctx, recipientID)
}

// MarkRead marks id read on recipientID's own behalf. Returns
// domain.ErrNotificationNotFound when id is unknown, or exists but does
// not belong to recipientID (see notificationStore's own doc for why the
// two are indistinguishable on purpose).
func (s *InboxService) MarkRead(ctx context.Context, recipientID identity.UserID, id domain.NotificationID) error {
	return s.store.MarkRead(ctx, recipientID, id)
}
