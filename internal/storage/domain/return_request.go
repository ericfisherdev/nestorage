package domain

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// MaxReturnRequestMessageRunes bounds ValidateReturnRequestMessage's
// accepted message length, measured in runes (not bytes) so a message whose
// first characters are multi-byte UTF-8 is not truncated mid-character by a
// byte-length check — mirrors MaxItemLinkLabelRunes's own rationale.
const MaxReturnRequestMessageRunes = 500

// ValidateReturnRequestMessage reports whether message is well-formed: nil
// (no message supplied) is always valid, wrapping
// ErrInvalidReturnRequestMessage for a non-nil message that is blank
// (whitespace-only) or longer than MaxReturnRequestMessageRunes — the
// domain mirror of return_request_message_check's "NULL or non-blank" rule.
func ValidateReturnRequestMessage(message *string) error {
	if message == nil {
		return nil
	}
	if strings.TrimSpace(*message) == "" {
		return fmt.Errorf("%w: message must not be blank", ErrInvalidReturnRequestMessage)
	}
	if utf8.RuneCountInString(*message) > MaxReturnRequestMessageRunes {
		return fmt.Errorf("%w: message is too long", ErrInvalidReturnRequestMessage)
	}
	return nil
}

// ReturnRequestStatus is a return request's lifecycle state, mirroring
// Visibility's own typed-enum shape: a bare string would let an unknown
// value slip through unnoticed until it reached the database CHECK.
type ReturnRequestStatus string

// The three states a return request can be in. Fulfilled and cancelled are
// both terminal — ReturnRequest.Fulfill/Cancel each reject a request that
// is not currently open.
const (
	ReturnRequestStatusOpen      ReturnRequestStatus = "open"
	ReturnRequestStatusFulfilled ReturnRequestStatus = "fulfilled"
	ReturnRequestStatusCancelled ReturnRequestStatus = "cancelled"
)

// Valid reports whether s is a known ReturnRequestStatus.
func (s ReturnRequestStatus) Valid() bool {
	switch s {
	case ReturnRequestStatusOpen, ReturnRequestStatusFulfilled, ReturnRequestStatusCancelled:
		return true
	default:
		return false
	}
}

// String returns the status's stored value.
func (s ReturnRequestStatus) String() string { return string(s) }

// ParseReturnRequestStatus validates and returns a ReturnRequestStatus, or a
// wrapped ErrInvalidReturnRequestStatus naming the offending value,
// mirroring ParseVisibility/ParseEventKind's own shape.
func ParseReturnRequestStatus(v string) (ReturnRequestStatus, error) {
	s := ReturnRequestStatus(v)
	if !s.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidReturnRequestStatus, v)
	}
	return s, nil
}

// ReturnRequest is a household member's ask for a checked-out item back: who
// is asking (RequesterID), who held the item when they asked (HolderID — a
// snapshot, not re-derived at read time; see 00013_return_request.sql's own
// comment for why that is safe), an optional Message, and the three-state
// lifecycle Status/Fulfill/Cancel drive. ResolvedAt is set exactly when
// Status is no longer open — the domain mirror of
// return_request_resolved_check. A plain struct, like Item and Bin: no
// logic beyond Fulfill/Cancel lives on it directly.
type ReturnRequest struct {
	ID          ReturnRequestID
	ItemID      ItemID
	RequesterID identity.UserID
	HolderID    identity.UserID
	Message     *string
	Status      ReturnRequestStatus
	CreatedAt   time.Time
	ResolvedAt  *time.Time
}

// Fulfill transitions r to fulfilled at the given time — the in-memory
// mirror of FulfillOpenForItem's UPDATE. OperationService's own transition
// calls the repository method directly rather than this guard (fulfilment
// flips every open request on an item in one statement, not a single loaded
// struct), but the guard is still defined here, alongside Cancel, so the
// state machine's rule lives in exactly one place regardless of which
// caller drives which transition. Returns ErrReturnRequestNotOpen, leaving r
// unmodified, if r is not currently open.
func (r *ReturnRequest) Fulfill(at time.Time) error {
	if r.Status != ReturnRequestStatusOpen {
		return ErrReturnRequestNotOpen
	}
	r.Status, r.ResolvedAt = ReturnRequestStatusFulfilled, &at
	return nil
}

// Cancel transitions r to cancelled at the given time — the withdrawal a
// requester drives themselves. Returns ErrReturnRequestNotOpen, leaving r
// unmodified, if r is not currently open.
func (r *ReturnRequest) Cancel(at time.Time) error {
	if r.Status != ReturnRequestStatusOpen {
		return ErrReturnRequestNotOpen
	}
	r.Status, r.ResolvedAt = ReturnRequestStatusCancelled, &at
	return nil
}

// ReturnRequestRepository is the outbound port for NSTR-43's return-request
// lifecycle. Implementations live in the adapter package, bound to a
// nestcore db.TX so both app.ReturnRequestService's own writers (Create,
// Cancel) and app.OperationService's fulfilment (FulfillOpenForItem) can
// each run inside their own transaction, alongside the item_event row that
// records the change — see ReturnRequestTxStores/OperationStores' own docs
// (internal/storage/app) for which narrow slice of this port each uses.
//
// Persistence contracts (the caller sets identity and valid fields; the
// store sets timestamps):
//   - Create expects r.ID, a validated r.Message (see
//     ValidateReturnRequestMessage), r.ItemID/r.RequesterID/r.HolderID, and
//     r.Status left at ReturnRequestStatusOpen; it populates r.CreatedAt.
//   - Cancel and FulfillOpenForItem transition matching rows and return
//     them with Status/ResolvedAt already updated — no separate
//     read-then-write round trip for the caller.
//
// Error contracts:
//   - Create returns ErrDuplicateReturnRequest when requesterID already has
//     an open request on itemID (return_request_open_uniq), or a wrapped
//     ErrItemNotFound/identity.ErrUserNotFound from the row's foreign keys.
//   - Cancel's WHERE clause enforces ownership itself (id AND
//     requester_id): it returns ErrReturnRequestNotFound when no row
//     matches both — an unknown id and one belonging to a different
//     requester are indistinguishable, the same masking ErrItemNotFound's
//     own doc requires — or ErrReturnRequestNotOpen when the matching row
//     exists but is no longer open.
//   - ListByItem and FulfillOpenForItem return an empty slice, not an
//     error, when itemID has no matching rows.
type ReturnRequestRepository interface {
	Create(ctx context.Context, r *ReturnRequest) error
	// ListByItem returns itemID's requests (every status) newest-first
	// (created_at DESC, id DESC), viewer-independent — the caller has
	// already authorized itemID's visibility. Returns an empty slice, not
	// an error, when itemID has none.
	ListByItem(ctx context.Context, itemID ItemID) ([]ReturnRequest, error)
	// Cancel withdraws id on requesterID's behalf. See the interface's own
	// error contract above.
	Cancel(ctx context.Context, id ReturnRequestID, requesterID identity.UserID) (*ReturnRequest, error)
	// FulfillOpenForItem flips every open request on itemID to fulfilled at
	// the given time, returning the flipped rows so the caller's notifier
	// can fan out without a second query.
	FulfillOpenForItem(ctx context.Context, itemID ItemID, at time.Time) ([]ReturnRequest, error)
}
