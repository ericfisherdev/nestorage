package domain

import (
	"context"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// EmailMessage is one fully rendered email, ready to hand to an
// EmailSender — assembled by app.Dispatcher from a claimed Notification's
// own Title/Body (via templates.go's emailSubject/emailTextBody/
// emailHTMLBody builders) plus the recipient's resolved address. Never
// persisted itself: NSTR-44's 00014 carries no column for a second,
// email-specific body, so no new migration is needed for it (Sprint 6
// decision) — Subject/TextBody reuse the same Title/Body every channel
// already shares, and HTMLBody is a minimal derivation of TextBody, not a
// separately stored value.
type EmailMessage struct {
	To       string
	Subject  string
	TextBody string
	HTMLBody string
}

// EmailSender delivers one rendered message. Implementations must be safe
// for concurrent use by Dispatcher.
//
// Returns ErrEmailNotConfigured when the sender cannot deliver at all
// (terminal — Dispatcher marks the row failed immediately, without
// spending any of its retry budget); any other error is treated as
// transient and retried, with backoff, until MaxAttempts is reached.
type EmailSender interface {
	Send(ctx context.Context, msg EmailMessage) error
}

// EmailAddressResolver is the narrow, cross-context port (ISP) Dispatcher
// uses to resolve a claimed row's RecipientID into a current email address
// at send time — declared here rather than depending on identity/domain's
// fuller UserRepository. Its one method's signature is deliberately
// identical to UserRepository.FindByID, so identity/adapter.UserRepository
// satisfies this port STRUCTURALLY: the composition root (cmd/server/main.go)
// passes the same identityRepo value it already constructs for every other
// identity-consuming service, with no adapter wrapper needed — mirroring
// cmd/server's own shellMemberLister/shellBinLister convention for a
// narrower, locally-declared read port over a fuller repository.
//
// A resolution failure (unknown user, or any other error) is treated by
// Dispatcher as terminal, never retried: RecipientID is always a real
// identity.UserID by construction (Notifier.notify already rejects a zero
// recipient before a row is ever enqueued — domain.ErrNoRecipient's own
// doc — and the account api key's integration principal is never a
// recipient for the identical reason), so a resolution failure here means
// the user record itself is gone, a condition retrying cannot fix.
type EmailAddressResolver interface {
	FindByID(ctx context.Context, id identity.UserID) (*identity.User, error)
}
