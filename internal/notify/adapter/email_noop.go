package adapter

import (
	"context"
	"log/slog"

	"github.com/ericfisherdev/nestorage/internal/notify/domain"
)

// NoopEmailSender is the domain.EmailSender used when EMAIL_ENABLED is
// false (NSTR-89) — the safe, zero-config default. It logs the delivery
// attempt and succeeds with no external side effect: a deployment with
// email disabled must run with ZERO AWS dependency, so this type imports
// nothing from aws-sdk-go-v2 at all. app.Dispatcher still runs
// unconditionally against this sender (see that type's own doc), which is
// what actually drains any pending email row a fresh install accumulates.
type NoopEmailSender struct {
	logger *slog.Logger
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.EmailSender = (*NoopEmailSender)(nil)

// NewNoopEmailSender constructs a NoopEmailSender with an injected logger.
func NewNoopEmailSender(logger *slog.Logger) *NoopEmailSender {
	if logger == nil {
		panic("notify/adapter: NewNoopEmailSender requires a non-nil logger")
	}
	return &NoopEmailSender{logger: logger}
}

// Send logs the would-be delivery and returns nil with no external side
// effect. No PII (the destination address, subject, or body) is logged;
// only body/subject lengths are recorded, mirroring Nestova's own
// NoopEmailSender no-PII-in-logs convention.
func (s *NoopEmailSender) Send(ctx context.Context, msg domain.EmailMessage) error {
	s.logger.InfoContext(ctx, "email send skipped: EMAIL_ENABLED is false",
		"subject_length", len(msg.Subject),
		"text_body_length", len(msg.TextBody),
		"html_body_length", len(msg.HTMLBody),
	)
	return nil
}
