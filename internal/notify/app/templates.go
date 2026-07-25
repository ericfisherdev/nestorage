package app

import storageapp "github.com/ericfisherdev/nestorage/internal/storage/app"

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
