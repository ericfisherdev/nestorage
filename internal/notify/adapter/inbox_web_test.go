package adapter_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/alexedwards/scs/v2"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/notify/adapter"
	"github.com/ericfisherdev/nestorage/internal/notify/domain"
)

// fakeInboxService is a configurable inboxService fake for InboxWebHandlers'
// hermetic unit tests, mirroring storage/adapter's own fake-per-port
// convention.
type fakeInboxService struct {
	notifications []domain.Notification
	listErr       error
	unreadCount   int
	unreadErr     error
	markReadErr   error

	markReadCalls []domain.NotificationID
}

func (f *fakeInboxService) List(_ context.Context, _ identity.UserID) ([]domain.Notification, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.notifications, nil
}

func (f *fakeInboxService) UnreadCount(_ context.Context, _ identity.UserID) (int, error) {
	if f.unreadErr != nil {
		return 0, f.unreadErr
	}
	return f.unreadCount, nil
}

func (f *fakeInboxService) MarkRead(_ context.Context, _ identity.UserID, id domain.NotificationID) error {
	f.markReadCalls = append(f.markReadCalls, id)
	return f.markReadErr
}

// inboxWebHarness bundles a running InboxWebHandlers server and a client
// carrying its session cookie across requests, mirroring storage/adapter's
// own itemsWebHarness shape.
type inboxWebHarness struct {
	server *httptest.Server
	client *http.Client
	inbox  *fakeInboxService
}

func newInboxWebHarness(t *testing.T, viewer identity.Principal, inbox *fakeInboxService) *inboxWebHarness {
	t.Helper()
	sm := scs.New()
	handlers := adapter.NewInboxWebHandlers(adapter.InboxWebHandlersDeps{
		Inbox: inbox, SM: sm, Layout: testLayout, Logger: testLogger(),
	})
	server := newPrincipalServer(t, sm, viewer, handlers.Routes)
	return &inboxWebHarness{server: server, client: newCSRFClient(t), inbox: inbox}
}

func (h *inboxWebHarness) get(t *testing.T, path string, htmx bool) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.server.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest %s: %v", path, err)
	}
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(body)
}

// seededUnreadNotification returns one unread (zero-value ReadAt)
// notification — NotificationInbox only renders a mark-read form (carrying
// the hidden csrf_token field getCSRF scrapes) for an unread row, so any
// test that needs a real CSRF token off of GET /notifications must seed at
// least one.
func seededUnreadNotification() []domain.Notification {
	return []domain.Notification{{ID: domain.NewNotificationID(), Title: "Return requested: Camping stove", Body: "Alex asked for it back."}}
}

func (h *inboxWebHarness) getCSRF(t *testing.T, path string) string {
	t.Helper()
	_, body := h.get(t, path, false)
	m := csrfRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no CSRF token in form:\n%s", body)
	}
	return m[1]
}

func (h *inboxWebHarness) postForm(t *testing.T, path string, form url.Values, htmx bool) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.server.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(body)
}

func TestNewInboxWebHandlers_PanicsOnNilDeps(t *testing.T) {
	inbox := &fakeInboxService{}
	sm := scs.New()

	tests := []struct {
		name string
		deps adapter.InboxWebHandlersDeps
	}{
		{"nil inbox", adapter.InboxWebHandlersDeps{Inbox: nil, SM: sm, Layout: testLayout, Logger: testLogger()}},
		{"nil sm", adapter.InboxWebHandlersDeps{Inbox: inbox, SM: nil, Layout: testLayout, Logger: testLogger()}},
		{"nil layout", adapter.InboxWebHandlersDeps{Inbox: inbox, SM: sm, Layout: nil, Logger: testLogger()}},
		{"nil logger", adapter.InboxWebHandlersDeps{Inbox: inbox, SM: sm, Layout: testLayout, Logger: nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewInboxWebHandlers(%s) did not panic", tt.name)
				}
			}()
			adapter.NewInboxWebHandlers(tt.deps)
		})
	}
}

func TestInboxWebHandlers_Badge_RendersUnreadCount(t *testing.T) {
	inbox := &fakeInboxService{unreadCount: 3}
	h := newInboxWebHarness(t, testViewer(), inbox)

	resp, body := h.get(t, "/notifications/badge", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /notifications/badge status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "3") {
		t.Errorf("badge body = %q, want it to show the unread count 3", body)
	}
	if !strings.Contains(body, "notifications-badge") {
		t.Errorf("badge body = %q, want the notifications-badge DOM id", body)
	}
}

func TestInboxWebHandlers_Badge_ZeroUnread_NoNumeral(t *testing.T) {
	inbox := &fakeInboxService{unreadCount: 0}
	h := newInboxWebHarness(t, testViewer(), inbox)

	_, body := h.get(t, "/notifications/badge", true)
	if strings.Contains(body, "no unread") == false && !strings.Contains(body, "aria-label") {
		t.Errorf("badge body = %q, want an accessible no-unread label", body)
	}
}

func TestInboxWebHandlers_Badge_ServiceError_Returns500(t *testing.T) {
	inbox := &fakeInboxService{unreadErr: errors.New("db down")}
	h := newInboxWebHarness(t, testViewer(), inbox)

	resp, _ := h.get(t, "/notifications/badge", true)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("GET /notifications/badge status = %d, want 500", resp.StatusCode)
	}
}

func TestInboxWebHandlers_Inbox_FullNavigation_WrapsInLayout(t *testing.T) {
	inbox := &fakeInboxService{}
	h := newInboxWebHarness(t, testViewer(), inbox)

	_, body := h.get(t, "/notifications", false)
	if !strings.Contains(body, "<layout>") {
		t.Errorf("full navigation body = %q, want it wrapped in the layout", body)
	}
}

func TestInboxWebHandlers_Inbox_HTMXRequest_NoLayout(t *testing.T) {
	inbox := &fakeInboxService{}
	h := newInboxWebHarness(t, testViewer(), inbox)

	_, body := h.get(t, "/notifications", true)
	if strings.Contains(body, "<layout>") {
		t.Errorf("HTMX request body = %q, want no layout wrapper", body)
	}
	if !strings.Contains(body, "notifications-inbox") {
		t.Errorf("body = %q, want the notifications-inbox DOM id", body)
	}
}

func TestInboxWebHandlers_Inbox_ListsNotifications(t *testing.T) {
	inbox := &fakeInboxService{notifications: []domain.Notification{
		{ID: domain.NewNotificationID(), Title: "Return requested: Camping stove", Body: "Alex asked for it back."},
	}}
	h := newInboxWebHarness(t, testViewer(), inbox)

	_, body := h.get(t, "/notifications", true)
	if !strings.Contains(body, "Return requested: Camping stove") {
		t.Errorf("body = %q, want the notification's own title", body)
	}
}

func TestInboxWebHandlers_Inbox_ServiceError_Returns500(t *testing.T) {
	inbox := &fakeInboxService{listErr: errors.New("db down")}
	h := newInboxWebHarness(t, testViewer(), inbox)

	resp, _ := h.get(t, "/notifications", false)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("GET /notifications status = %d, want 500", resp.StatusCode)
	}
}

func TestInboxWebHandlers_MarkRead_Success_TriggersRefetchAndRerenders(t *testing.T) {
	inbox := &fakeInboxService{notifications: seededUnreadNotification()}
	h := newInboxWebHarness(t, testViewer(), inbox)
	csrf := h.getCSRF(t, "/notifications")

	id := domain.NewNotificationID()
	form := url.Values{"csrf_token": {csrf}}
	resp, body := h.postForm(t, "/notifications/"+id.String()+"/read", form, true)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST mark-read status = %d, want 200", resp.StatusCode)
	}
	if len(inbox.markReadCalls) != 1 || inbox.markReadCalls[0] != id {
		t.Errorf("MarkRead calls = %v, want exactly [%v]", inbox.markReadCalls, id)
	}
	if got := resp.Header.Get("HX-Trigger"); got != "notifications-updated" {
		t.Errorf("HX-Trigger header = %q, want \"notifications-updated\"", got)
	}
	if !strings.Contains(body, "notifications-inbox") {
		t.Errorf("body = %q, want the re-rendered notifications-inbox fragment", body)
	}
}

func TestInboxWebHandlers_MarkRead_CSRFRejected(t *testing.T) {
	inbox := &fakeInboxService{}
	h := newInboxWebHarness(t, testViewer(), inbox)

	id := domain.NewNotificationID()
	form := url.Values{"csrf_token": {"wrong-token"}}
	resp, _ := h.postForm(t, "/notifications/"+id.String()+"/read", form, true)

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST mark-read with a bad CSRF token status = %d, want 403", resp.StatusCode)
	}
	if len(inbox.markReadCalls) != 0 {
		t.Error("MarkRead must not be called when CSRF verification fails")
	}
}

func TestInboxWebHandlers_MarkRead_NotFound(t *testing.T) {
	inbox := &fakeInboxService{notifications: seededUnreadNotification(), markReadErr: domain.ErrNotificationNotFound}
	h := newInboxWebHarness(t, testViewer(), inbox)
	csrf := h.getCSRF(t, "/notifications")

	id := domain.NewNotificationID()
	form := url.Values{"csrf_token": {csrf}}
	resp, _ := h.postForm(t, "/notifications/"+id.String()+"/read", form, true)

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST mark-read (unknown/not-owned id) status = %d, want 404", resp.StatusCode)
	}
}

func TestInboxWebHandlers_MarkRead_MalformedID_Returns400(t *testing.T) {
	inbox := &fakeInboxService{notifications: seededUnreadNotification()}
	h := newInboxWebHarness(t, testViewer(), inbox)
	csrf := h.getCSRF(t, "/notifications")

	form := url.Values{"csrf_token": {csrf}}
	resp, _ := h.postForm(t, "/notifications/not-a-uuid/read", form, true)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST mark-read (malformed id) status = %d, want 400", resp.StatusCode)
	}
}

func TestInboxWebHandlers_MarkRead_UnexpectedError_Returns500(t *testing.T) {
	inbox := &fakeInboxService{notifications: seededUnreadNotification(), markReadErr: errors.New("db down")}
	h := newInboxWebHarness(t, testViewer(), inbox)
	csrf := h.getCSRF(t, "/notifications")

	id := domain.NewNotificationID()
	form := url.Values{"csrf_token": {csrf}}
	resp, _ := h.postForm(t, "/notifications/"+id.String()+"/read", form, true)

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("POST mark-read (unexpected error) status = %d, want 500", resp.StatusCode)
	}
}
