package app

import (
	"context"

	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// EventAppender is the narrow port (ISP) every transactional closure in
// this package appends its event through: the one method NSTR-40's
// tx-bound ItemEventRepository exposes (a superset). Bound to the same
// pgx transaction as the placement/lifecycle change it records, this is
// what makes atomicity possible — the closure returns the resulting error
// (never swallows it) so the caller's WithinTx/WithinBinTx/WithinItemTx
// rolls the whole transaction back, undoing the state change too.
type EventAppender interface {
	Append(ctx context.Context, e *domain.ItemEvent) error
}

// binLabel formats a bin's display label exactly the way
// storage/adapter's buildBinOptions does (bins_web.go: "code — name") —
// the event log's bin_label snapshot mirrors the UI's own convention
// rather than inventing a second one.
func binLabel(bin *domain.Bin) string {
	return bin.Code + " — " + bin.Name
}

// noteValue dereferences an operation's optional note into
// item_event.note's plain-string contract: nil (no note supplied) becomes
// "", matching domain.ItemEvent's own "empty string means unset" rule for
// its other optional string fields (see ItemEvent's own doc).
func noteValue(note *string) string {
	if note == nil {
		return ""
	}
	return *note
}
