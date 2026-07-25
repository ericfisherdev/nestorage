package adapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestcore/render"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/notify/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
	"github.com/ericfisherdev/nestorage/web/components"
)

// errInternalServerError is the generic message every unexpected failure in
// this package's web handlers answers with — mirrors storage/adapter's own
// constant of the same name (unexported to its package, so it cannot be
// reused directly here).
const errInternalServerError = "internal server error"

// requestLayoutFunc wraps page content in the app shell for a specific
// request — mirrors storage/adapter's own identical, unexported type (see
// that package's web_common.go for the full rationale; neither can be
// reused directly across bounded-context packages). Injected by the
// composition root (cmd/server/shell.go).
type requestLayoutFunc func(r *http.Request, content templ.Component) templ.Component

// inboxService is the narrow port (ISP) InboxWebHandlers depends on,
// satisfied by *app.InboxService.
type inboxService interface {
	List(ctx context.Context, recipientID identity.UserID) ([]domain.Notification, error)
	UnreadCount(ctx context.Context, recipientID identity.UserID) (int, error)
	MarkRead(ctx context.Context, recipientID identity.UserID, id domain.NotificationID) error
}

// InboxWebHandlers serves the sidebar's unread badge and the /notifications
// inbox screen — the household member's own view of NSTR-44's in-app
// delivery. Mirrors ItemsWebHandlers' own shape: a narrow service
// interface (ISP), an injected requestLayoutFunc, the session manager for
// CSRF on the one mutating route (MarkRead), a logger; the constructor
// panics on any nil dependency (DIP + fail-fast). Every route is mounted
// on the composition root's own authenticated mux (RequireAuthenticated) —
// any signed-in household member reads and marks read only their own
// inbox (InboxService's own recipientID scoping).
type InboxWebHandlers struct {
	inbox  inboxService
	sm     *scs.SessionManager
	layout requestLayoutFunc
	logger *slog.Logger
}

// InboxWebHandlersDeps groups NewInboxWebHandlers' dependencies into one
// value instead of a growing parameter list, mirroring
// ItemsWebHandlersDeps' own rationale. Every field is still injected
// explicitly by the composition root (cmd/server/main.go).
type InboxWebHandlersDeps struct {
	Inbox  inboxService
	SM     *scs.SessionManager
	Layout requestLayoutFunc
	Logger *slog.Logger
}

// NewInboxWebHandlers constructs InboxWebHandlers. Every dependency is
// required; a missing one panics at construction time, matching every
// other WebHandlers constructor in this codebase.
func NewInboxWebHandlers(deps InboxWebHandlersDeps) *InboxWebHandlers {
	if deps.Inbox == nil {
		panic("notify/adapter: NewInboxWebHandlers requires a non-nil inboxService")
	}
	if deps.SM == nil {
		panic("notify/adapter: NewInboxWebHandlers requires a non-nil session manager")
	}
	if deps.Layout == nil {
		panic("notify/adapter: NewInboxWebHandlers requires a non-nil requestLayoutFunc")
	}
	if deps.Logger == nil {
		panic("notify/adapter: NewInboxWebHandlers requires a non-nil logger")
	}
	return &InboxWebHandlers{inbox: deps.Inbox, sm: deps.SM, layout: deps.Layout, logger: deps.Logger}
}

// Routes registers this handler group's three routes on mux.
func (h *InboxWebHandlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /notifications", h.Inbox)
	mux.HandleFunc("GET /notifications/badge", h.Badge)
	mux.HandleFunc("POST /notifications/{id}/read", h.MarkRead)
}

// Inbox handles GET /notifications: the signed-in viewer's own notification
// list, either a bare fragment (hx-request, e.g. after MarkRead's own
// outerHTML swap) or a full-page navigation wrapped in the app shell —
// mirrors ItemsWebHandlers.History's identical IsHTMX branch.
func (h *InboxWebHandlers) Inbox(w http.ResponseWriter, r *http.Request) {
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	notifications, err := h.inbox.List(r.Context(), viewer.UserID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "notify: list notifications", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}

	view := buildNotificationInboxView(notifications, session.CSRFToken(r.Context(), h.sm), time.Local)
	content := components.NotificationInbox(view)
	if !render.IsHTMX(r) {
		content = h.layout(r, content)
	}
	if err := render.Render(r.Context(), w, http.StatusOK, content); err != nil {
		h.logger.ErrorContext(r.Context(), "notify: render inbox", "error", err)
	}
}

// Badge handles GET /notifications/badge: NotificationBellStub's own
// hx-get target, fired on every page load and again whenever MarkRead's
// response dispatches the notifications-updated event. Always a bare
// fragment — the badge only ever swaps into its own #notifications-badge
// node, mirroring ItemHistoryStub's own "stub renders nothing, the real
// content is server-rendered on the stub's own hx-get" shape.
func (h *InboxWebHandlers) Badge(w http.ResponseWriter, r *http.Request) {
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	count, err := h.inbox.UnreadCount(r.Context(), viewer.UserID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "notify: unread count", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	content := components.NotificationBadge(components.NotificationBadgeView{UnreadCount: count})
	if err := render.Render(r.Context(), w, http.StatusOK, content); err != nil {
		h.logger.ErrorContext(r.Context(), "notify: render badge", "error", err)
	}
}

// MarkRead handles POST /notifications/{id}/read: CSRF, InboxService.MarkRead
// on the viewer's own behalf, then re-render the whole inbox fragment
// either way (mirroring RequestReturn's identical "re-render the whole
// fragment either way" convention) plus an HX-Trigger so the sidebar
// badge — listening via hx-trigger="load, notifications-updated
// from:body" — refetches and shrinks without a full page reload.
func (h *InboxWebHandlers) MarkRead(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathNotificationID(w, r)
	if !ok {
		return
	}
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	if err := h.inbox.MarkRead(r.Context(), viewer.UserID, id); err != nil {
		if errors.Is(err, domain.ErrNotificationNotFound) {
			http.Error(w, "notification not found", http.StatusNotFound)
			return
		}
		h.logger.ErrorContext(r.Context(), "notify: mark read", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Trigger", "notifications-updated")
	h.renderInboxFragment(w, r, viewer.UserID)
}

// renderInboxFragment re-renders MarkRead's own #notifications-inbox target
// fragment — always a bare fragment, never layout-wrapped, since it only
// ever swaps into a page the viewer is already on.
func (h *InboxWebHandlers) renderInboxFragment(w http.ResponseWriter, r *http.Request, recipientID identity.UserID) {
	notifications, err := h.inbox.List(r.Context(), recipientID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "notify: list notifications", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	view := buildNotificationInboxView(notifications, session.CSRFToken(r.Context(), h.sm), time.Local)
	if err := render.Render(r.Context(), w, http.StatusOK, components.NotificationInbox(view)); err != nil {
		h.logger.ErrorContext(r.Context(), "notify: render inbox", "error", err)
	}
}

// pathNotificationID parses the {id} path value, answering 400 and
// reporting ok=false on a malformed one — mirrors pathReturnRequestID's
// own shape.
func (h *InboxWebHandlers) pathNotificationID(w http.ResponseWriter, r *http.Request) (domain.NotificationID, bool) {
	id, err := domain.ParseNotificationID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return domain.NotificationID{}, false
	}
	return id, true
}

// verifyRequest parses the form and verifies the CSRF token — mirrors
// storage/adapter's own identical, unexported verifyRequest (neither can
// be reused directly across bounded-context packages).
func verifyRequest(w http.ResponseWriter, r *http.Request, sm *scs.SessionManager) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	if !session.VerifyCSRF(r, sm) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return false
	}
	return true
}

// buildNotificationInboxView projects notifications into
// NotificationInbox's view model, newest first (ListForRecipient's own
// ordering, preserved here). loc is the zone formatNotificationTime's
// "local" means — production passes time.Local, a test constructs its own
// fixed zone, mirroring buildItemHistoryDays' identical "never the
// process's own ambient zone" rationale.
func buildNotificationInboxView(notifications []domain.Notification, csrfToken string, loc *time.Location) components.NotificationInboxView {
	items := make([]components.NotificationItemView, 0, len(notifications))
	for _, n := range notifications {
		items = append(items, components.NotificationItemView{
			ID: n.ID.String(), Title: n.Title, Body: n.Body,
			Time: formatNotificationTime(n.CreatedAt, loc), Unread: n.ReadAt == nil,
			CSRFToken: csrfToken,
		})
	}
	return components.NotificationInboxView{Items: items}
}

// formatNotificationTime renders a notification's own CreatedAt for
// display — the one place a notification row's time.Time is formatted, so
// the templ layer never receives one directly (formatStoredDate/
// formatHistoryTime's own identical contract).
func formatNotificationTime(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("Jan 2, 3:04 PM")
}
