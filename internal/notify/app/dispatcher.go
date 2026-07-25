package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ericfisherdev/nestorage/internal/notify/domain"
)

// dispatcherBackoffBase and dispatcherBackoffMax bound Dispatcher's own
// exponential backoff between send attempts on a single row:
// dispatcherBackoffBase doubled per prior attempt, capped at
// dispatcherBackoffMax. Fixed constants, not configuration —
// EMAIL_MAX_ATTEMPTS (the attempt COUNT cap) is the one retry knob this
// ticket exposes; the shape of the backoff curve itself needs no separate
// env var for a fixed, small household deployment.
const (
	dispatcherBackoffBase = time.Minute
	dispatcherBackoffMax  = 30 * time.Minute
	// dispatcherBackoffMaxShift caps how far attempts may shift
	// dispatcherBackoffBase left before backoffDuration just returns
	// dispatcherBackoffMax directly — defends against an oversized or
	// undefined shift if attempts is ever unexpectedly large.
	dispatcherBackoffMaxShift = 10
)

// Dispatcher drains NSTR-44's Outbox of pending email rows (NSTR-89): each
// tick it claims up to batchSize due rows (Outbox.ClaimDue), resolves each
// row's recipient to a current email address, renders subject/text/html
// from the row's own Title/Body (templates.go's emailSubject/emailTextBody/
// emailHTMLBody), and hands the result to sender. A send failure
// reschedules that ONE row with exponential backoff up to maxAttempts, then
// marks it StatusFailed — never poisoning the rest of the batch (see
// dispatchOnce's own doc) and never affecting the sibling in-app row for
// the same event, which NSTR-44 already delivered synchronously at enqueue
// time, independent of this loop entirely (Sprint 6 decision: "a terminal
// failure needs no fallback re-enqueue").
type Dispatcher struct {
	outbox      domain.Outbox
	resolver    domain.EmailAddressResolver
	sender      domain.EmailSender
	clock       func() time.Time
	interval    time.Duration
	batchSize   int
	maxAttempts int
	logger      *slog.Logger
}

// NewDispatcher constructs Dispatcher. Every dependency is required, and
// interval/batchSize/maxAttempts must be positive; a violation panics at
// construction time, matching every other constructor in this codebase.
// config.EmailConfig.Validate is what actually catches a misconfigured
// value before it ever reaches here (see cmd/server/main.go's own wiring) —
// this is a last-resort guard against passing raw zero values directly,
// most importantly because time.NewTicker itself panics on a non-positive
// interval, and a bare stdlib panic there would be far less legible than
// this one.
func NewDispatcher(
	outbox domain.Outbox,
	resolver domain.EmailAddressResolver,
	sender domain.EmailSender,
	clock func() time.Time,
	interval time.Duration,
	batchSize, maxAttempts int,
	logger *slog.Logger,
) *Dispatcher {
	if outbox == nil {
		panic("notify/app: NewDispatcher requires a non-nil domain.Outbox")
	}
	if resolver == nil {
		panic("notify/app: NewDispatcher requires a non-nil domain.EmailAddressResolver")
	}
	if sender == nil {
		panic("notify/app: NewDispatcher requires a non-nil domain.EmailSender")
	}
	if clock == nil {
		panic("notify/app: NewDispatcher requires a non-nil clock")
	}
	if interval <= 0 {
		panic("notify/app: NewDispatcher requires a positive interval")
	}
	if batchSize <= 0 {
		panic("notify/app: NewDispatcher requires a positive batchSize")
	}
	if maxAttempts <= 0 {
		panic("notify/app: NewDispatcher requires a positive maxAttempts")
	}
	if logger == nil {
		panic("notify/app: NewDispatcher requires a non-nil logger")
	}
	return &Dispatcher{
		outbox: outbox, resolver: resolver, sender: sender, clock: clock,
		interval: interval, batchSize: batchSize, maxAttempts: maxAttempts, logger: logger,
	}
}

// Run blocks, ticking every d.interval, claiming and delivering due email
// rows until ctx is cancelled. Started unconditionally by cmd/server/main.go
// regardless of EMAIL_ENABLED — see config.EmailConfig.Enabled's own doc for
// why: NSTR-45's preference screen can turn a user's email channel on while
// EMAIL_ENABLED is false, and Notifier consults the PREFERENCE, not the
// config flag, when deciding whether to write a pending row at all. Gating
// this loop on EMAIL_ENABLED would strand those rows pending forever; d's
// sender is already resolved by bootstrap.NewEmailSender to the no-op when
// disabled, so running unconditionally just drains them harmlessly instead.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.DispatchOnce(ctx)
		}
	}
}

// DispatchOnce claims one batch and delivers each row independently: a
// single row's failure is fully contained inside deliver (it logs and
// reschedules/fails, never panics or returns an error deliver's caller
// would have to react to), so it never stops this loop from reaching its
// siblings. Exported (rather than folded entirely into Run's private loop)
// so a gated test can drive exactly one batch synchronously against a real
// database without racing a real ticker.
func (d *Dispatcher) DispatchOnce(ctx context.Context) {
	notifications, err := d.outbox.ClaimDue(ctx, d.batchSize)
	if err != nil {
		d.logger.ErrorContext(ctx, "dispatch: claim due notifications", "error", err)
		return
	}
	for _, n := range notifications {
		d.deliver(ctx, n)
	}
}

// deliver resolves n's recipient, renders, and sends one email.
func (d *Dispatcher) deliver(ctx context.Context, n *domain.Notification) {
	recipient, err := d.resolver.FindByID(ctx, n.RecipientID)
	if err != nil {
		// See domain.EmailAddressResolver's own doc: RecipientID is always
		// a real user by construction, so a resolution failure here means
		// the record itself is gone — retrying cannot fix that.
		d.logger.ErrorContext(ctx, "dispatch: resolve recipient email", "error", err, "notification_id", n.ID.String())
		d.forceFail(ctx, n)
		return
	}

	msg := domain.EmailMessage{
		To:       recipient.Email,
		Subject:  emailSubject(n),
		TextBody: emailTextBody(n),
		HTMLBody: emailHTMLBody(n),
	}
	if err := d.sender.Send(ctx, msg); err != nil {
		if errors.Is(err, domain.ErrEmailNotConfigured) {
			d.logger.ErrorContext(ctx, "dispatch: email sender not configured", "notification_id", n.ID.String())
			d.forceFail(ctx, n)
			return
		}
		d.logger.WarnContext(ctx, "dispatch: send email failed, rescheduling", "error", err, "notification_id", n.ID.String(), "attempts", n.Attempts)
		d.reschedule(ctx, n)
		return
	}

	if err := d.outbox.MarkSent(ctx, n.ID, d.clock()); err != nil {
		d.logger.ErrorContext(ctx, "dispatch: mark sent", "error", err, "notification_id", n.ID.String())
	}
}

// reschedule walks n back to pending (under maxAttempts) or terminally
// failed (at or over it), via the exact port NSTR-44 already implements and
// gated-tests — this ticket adds no SQL and no repository method (Sprint 6
// decision).
func (d *Dispatcher) reschedule(ctx context.Context, n *domain.Notification) {
	next := d.clock().Add(backoffDuration(n.Attempts))
	if err := d.outbox.RescheduleOrFail(ctx, n.ID, next, d.maxAttempts); err != nil {
		d.logger.ErrorContext(ctx, "dispatch: reschedule or fail", "error", err, "notification_id", n.ID.String())
	}
}

// forceFail lands n on StatusFailed immediately, spending none of its
// retry budget — the terminal outcome both ErrEmailNotConfigured and an
// unresolvable recipient call for (see deliver's own doc). RescheduleOrFail
// fails a row once attempts+1 >= maxAttempts; passing 0 makes that true on
// the very first call regardless of n.Attempts, reusing the existing port
// rather than adding a MarkFailed method this ticket has no SQL budget for.
func (d *Dispatcher) forceFail(ctx context.Context, n *domain.Notification) {
	if err := d.outbox.RescheduleOrFail(ctx, n.ID, d.clock(), 0); err != nil {
		d.logger.ErrorContext(ctx, "dispatch: force fail", "error", err, "notification_id", n.ID.String())
	}
}

// backoffDuration returns the delay before the next attempt after attempts
// prior failures: dispatcherBackoffBase doubled per attempt, capped at
// dispatcherBackoffMax. attempts is clamped before shifting to guard
// against an oversized or negative shift count.
func backoffDuration(attempts int) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	if attempts > dispatcherBackoffMaxShift {
		return dispatcherBackoffMax
	}
	dur := dispatcherBackoffBase << attempts
	if dur <= 0 || dur > dispatcherBackoffMax {
		return dispatcherBackoffMax
	}
	return dur
}
