package app

import (
	"html"
	"strings"

	"github.com/ericfisherdev/nestorage/internal/notify/domain"
	storageapp "github.com/ericfisherdev/nestorage/internal/storage/app"
)

// templates.go builds the in-app title/body pair for both event types
// NSTR-44 raises — plain text only, rendered as-is by
// web/components.NotificationInbox. NSTR-89 appends the email
// subject/HTML builders alongside these, never rewriting them (Sprint 6
// decision: "the email ticket appends to that file").

// returnRequestedTitle builds EventTypeReturnRequested's title: the
// notification list's own scannable headline for the holder.
func returnRequestedTitle(n storageapp.ReturnRequestNotification) string {
	return "Return requested: " + n.ItemName
}

// returnRequestedBody builds EventTypeReturnRequested's body, naming the
// requester and, when supplied, appending their optional note.
func returnRequestedBody(n storageapp.ReturnRequestNotification) string {
	body := n.RequesterLabel + " asked for \"" + n.ItemName + "\" back."
	if n.Message != nil && *n.Message != "" {
		body += " Their note: \"" + *n.Message + "\""
	}
	return body
}

// itemReturnedTitle builds EventTypeItemReturned's title for the requester
// whose open request this fulfilled.
func itemReturnedTitle(n storageapp.ReturnRequestNotification) string {
	return n.ItemName + " is back"
}

// itemReturnedBody builds EventTypeItemReturned's body.
func itemReturnedBody(n storageapp.ReturnRequestNotification) string {
	return "\"" + n.ItemName + "\" was returned and is ready to check out again."
}

// emailSubject and emailTextBody build a claimed email row's own message
// content (NSTR-89) — appended here, not rewriting anything above. Both
// simply reuse the row's own Title/Body rather than re-deriving them from a
// storageapp.ReturnRequestNotification the dispatcher no longer has access
// to at send time (only the persisted domain.Notification survives from
// enqueue to claim): Notifier.enqueueChannel's own ChannelEmail case writes
// the SAME title/body the in-app row for the identical event already
// carries (return_requested/item_returned), built by the very functions
// above, so no separate per-event email copy is needed — event-specific
// content already happened once, upstream, at enqueue time.
func emailSubject(n *domain.Notification) string { return n.Title }

// emailTextBody returns n's plain-text email body — see emailSubject's own
// doc for why this reuses Body unchanged rather than a second template.
func emailTextBody(n *domain.Notification) string { return n.Body }

// emailHTMLBody wraps n's plain-text body in a minimal HTML document: one
// escaped paragraph with newlines turned into line breaks. Deliberately
// minimal — no branding, no styling, no per-event markup — since Body
// already carries every event-specific detail (item name, requester,
// optional note) as plain text; SES's own "Simple" content type sends both
// parts together so a recipient's client renders whichever it supports.
func emailHTMLBody(n *domain.Notification) string {
	escaped := html.EscapeString(n.Body)
	withBreaks := strings.ReplaceAll(escaped, "\n", "<br>")
	return "<!DOCTYPE html><html><body><p>" + withBreaks + "</p></body></html>"
}
