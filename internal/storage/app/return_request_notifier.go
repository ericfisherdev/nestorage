package app

import (
	"context"
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// ReturnRequestNotification carries everything a ReturnRequestNotifier
// implementation needs to tell a person about a return request, without
// reaching into storage's own repositories itself (NSTR-43's reconciliation
// R10) — NSTR-44 imports this type directly, the same "app depends on by
// name" arrangement OperationStores/ItemTxStores already establish for
// their own tx-bound stores.
type ReturnRequestNotification struct {
	RequestID domain.ReturnRequestID
	ItemID    domain.ItemID
	ItemName  string

	RequesterID    identity.UserID
	RequesterLabel string

	HolderID identity.UserID

	Message *string

	CreatedAt  time.Time
	ResolvedAt *time.Time
}

// ReturnRequestNotifier is the outbound port NSTR-44 implements: told about
// a newly-raised request (ReturnRequested, one notification, raised by
// app.ReturnRequestService.Request) and about every request a placement
// change just resolved (ReturnRequestsFulfilled, one call per operation,
// fanned out by app.OperationService.transition on any held-to-bin
// transition). Both calls are post-commit and fire-and-forget from the
// caller's perspective — neither returns an error, so an implementation
// logs its own delivery failures rather than letting one propagate back
// into an already-committed operation.
//
// These two Go method names are canonical (R10): they are distinct from,
// and must not be confused with, notify's own persisted EventType values
// (return_requested/item_returned in internal/notify/domain) — a different
// enum in a different bounded context that happens to reuse similar words.
// The mapping NSTR-44 implements is ReturnRequested -> EventType
// return_requested, ReturnRequestsFulfilled -> EventType item_returned.
type ReturnRequestNotifier interface {
	ReturnRequested(ctx context.Context, n ReturnRequestNotification)
	ReturnRequestsFulfilled(ctx context.Context, ns []ReturnRequestNotification)
}

// NopReturnRequestNotifier is the do-nothing ReturnRequestNotifier NSTR-43
// wires cmd/server/main.go with; NSTR-44 swaps in the real implementation at
// composition-root time (R10), with no change to ReturnRequestService or
// OperationService's own code — the same OCP seam identity's Revokers slice
// and media's PhotoStore backend selection already follow.
type NopReturnRequestNotifier struct{}

// ReturnRequested does nothing.
func (NopReturnRequestNotifier) ReturnRequested(context.Context, ReturnRequestNotification) {}

// ReturnRequestsFulfilled does nothing.
func (NopReturnRequestNotifier) ReturnRequestsFulfilled(context.Context, []ReturnRequestNotification) {
}
