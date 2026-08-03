package app

import (
	"context"
	"fmt"
	"log/slog"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// returnRequestItemGetter is the narrow port (ISP) ReturnRequestService
// depends on to authorize every operation against the owning item's
// visibility and to read its current name/holder for the return_request row
// and its item_event, satisfied by domain.ItemRepository (a superset, via
// Get) and by test fakes. Mirrors itemLinkItemGetter's identical rationale:
// a return request carries no visibility of its own, so every method below
// resolves itemID through this port FIRST, before ever touching a request.
type returnRequestItemGetter interface {
	Get(ctx context.Context, viewer identity.Principal, id domain.ItemID) (*domain.Item, error)
}

// returnRequestLister is the narrow port (ISP) ReturnRequestService depends
// on for its own non-transactional read, ListByItem, satisfied by
// domain.ReturnRequestRepository (a superset) bound to the shared pool.
// Kept separate from ReturnRequestLifecycleStore below (used only inside a
// returnRequestTransactor's closure): a plain read needs no transaction, the
// same reason itemEventRepo is constructed twice in cmd/server/main.go —
// once pool-bound for reads, once per-transaction for writers.
type returnRequestLister interface {
	ListByItem(ctx context.Context, householdID identity.HouseholdID, itemID domain.ItemID) ([]domain.ReturnRequest, error)
}

// ReturnRequestLifecycleStore is the narrow port (ISP)
// ReturnRequestTxStores.ReturnRequests exposes to a returnRequestTransactor's
// transactional closure: Create and Cancel, the two mutations
// ReturnRequestService itself drives. Fulfilment is deliberately absent —
// that is OperationService's own, differently-shaped ReturnRequestFulfiller
// port (operations.go), reached through OperationStores instead. Both
// methods here already exist on domain.ReturnRequestRepository (a
// superset). Exported because adapter.PostgresUnitOfWork must spell it by
// name to construct ReturnRequestTxStores.
type ReturnRequestLifecycleStore interface {
	Create(ctx context.Context, r *domain.ReturnRequest) error
	Cancel(ctx context.Context, householdID identity.HouseholdID, id domain.ReturnRequestID, requesterID identity.UserID) (*domain.ReturnRequest, error)
}

// ReturnRequestTxStores bundles the tx-bound stores a returnRequestTransactor's
// closure operates on: ReturnRequestLifecycleStore for the mutation,
// EventAppender for the return_requested/return_request_cancelled row that
// records it in the same transaction — NSTR-43's reconciliation R7:
// ReturnRequestService appends its own events, using NSTR-40's tx-bound
// appender inside its own transaction, never OperationService's (Request
// and Cancel are not placement operations and never reach
// OperationService). Mirrors ItemTxStores/OperationStores' own
// struct-bundle rationale. Exported because adapter.PostgresUnitOfWork must
// spell it by name.
type ReturnRequestTxStores struct {
	ReturnRequests ReturnRequestLifecycleStore
	Events         EventAppender
}

// returnRequestTransactor runs fn inside one transaction against a
// ReturnRequestTxStores bundle, committing when fn returns nil and rolling
// back — with no partial write, including the item_event row fn appended —
// on any error fn returns. The return-request analog of
// transactor/itemTransactor (operations.go/item.go). Named for its single
// WithinReturnRequestTx method, per the same single-method-interface
// convention those two follow.
type returnRequestTransactor interface {
	WithinReturnRequestTx(ctx context.Context, fn func(ReturnRequestTxStores) error) error
}

// ReturnRequestService implements NSTR-43's return-request lifecycle:
// request, cancel, and list, every one of them scoped to what viewer may see
// via returnRequestItemGetter. Request and Cancel each run inside one
// transaction (returnRequestTransactor) so the return_request row and the
// item_event row that records it commit or roll back together. Fulfilment
// is deliberately NOT implemented here: it happens inside
// OperationService's own transaction, on any held-to-bin placement change
// (see operations.go's own doc), never through this service — there is no
// user-facing "fulfill" action.
type ReturnRequestService struct {
	items    returnRequestItemGetter
	requests returnRequestLister
	uow      returnRequestTransactor
	notifier ReturnRequestNotifier
	logger   *slog.Logger
}

// NewReturnRequestService constructs ReturnRequestService. Every dependency
// is required; a missing one panics at construction time, matching every
// other constructor in this codebase (see NewItemLinkService).
func NewReturnRequestService(items returnRequestItemGetter, requests returnRequestLister, uow returnRequestTransactor, notifier ReturnRequestNotifier, logger *slog.Logger) *ReturnRequestService {
	if items == nil {
		panic("storage/app: NewReturnRequestService requires a non-nil returnRequestItemGetter")
	}
	if requests == nil {
		panic("storage/app: NewReturnRequestService requires a non-nil returnRequestLister")
	}
	if uow == nil {
		panic("storage/app: NewReturnRequestService requires a non-nil returnRequestTransactor")
	}
	if notifier == nil {
		panic("storage/app: NewReturnRequestService requires a non-nil ReturnRequestNotifier")
	}
	if logger == nil {
		panic("storage/app: NewReturnRequestService requires a non-nil logger")
	}
	return &ReturnRequestService{items: items, requests: requests, uow: uow, notifier: notifier, logger: logger}
}

// Request asks for itemID's return on actor's behalf: it records a
// return_request row and a return_requested item_event in one transaction,
// then notifies the holder best-effort, post-commit. Returns
// domain.ErrHolderRequired (unwrapped, a precondition checked before any
// store access — mirrors OperationService.RemoveFromBin's own check) when
// actor is not a real person, domain.ErrInvalidReturnRequestMessage from
// validation, a wrapped domain.ErrItemNotFound when itemID is unknown or not
// visible to actor, domain.ErrItemNotCheckedOut when itemID is currently
// sitting in a bin, domain.ErrRequesterHoldsItem when actor is itemID's
// current holder, or domain.ErrDuplicateReturnRequest when actor already has
// an open request on itemID.
func (s *ReturnRequestService) Request(ctx context.Context, actor identity.Principal, itemID domain.ItemID, message *string) (*domain.ReturnRequest, error) {
	if actor.Kind != identity.KindUser {
		return nil, domain.ErrHolderRequired
	}
	if err := domain.ValidateReturnRequestMessage(message); err != nil {
		return nil, err
	}

	it, err := s.items.Get(ctx, actor, itemID)
	if err != nil {
		return nil, fmt.Errorf("app: request return: %w", err)
	}
	if it.State() != domain.StateCheckedOut {
		return nil, domain.ErrItemNotCheckedOut
	}
	if *it.HeldBy == actor.UserID {
		return nil, domain.ErrRequesterHoldsItem
	}

	req := &domain.ReturnRequest{
		ID: domain.NewReturnRequestID(), HouseholdID: actor.HouseholdID, ItemID: itemID,
		RequesterID: actor.UserID, HolderID: *it.HeldBy,
		Message: message, Status: domain.ReturnRequestStatusOpen,
	}

	err = s.uow.WithinReturnRequestTx(ctx, func(stores ReturnRequestTxStores) error {
		if err := stores.ReturnRequests.Create(ctx, req); err != nil {
			return err
		}
		event := domain.NewItemEvent(domain.NewItemEventID(), itemID, it.Name, domain.EventReturnRequested, actor)
		if err := event.Validate(); err != nil {
			return err
		}
		return stores.Events.Append(ctx, &event)
	})
	if err != nil {
		return nil, fmt.Errorf("app: request return: %w", err)
	}

	s.logAction(ctx, "return requested", itemID, req.ID)
	s.notifier.ReturnRequested(ctx, ReturnRequestNotification{
		RequestID: req.ID, ItemID: itemID, ItemName: it.Name,
		RequesterID: actor.UserID, RequesterLabel: actor.Actor(),
		HolderID: req.HolderID, Message: req.Message, CreatedAt: req.CreatedAt,
	})
	return req, nil
}

// Cancel withdraws actor's own open return request requestID on itemID,
// recording a return_request_cancelled item_event in the same transaction.
// Returns a wrapped domain.ErrItemNotFound when itemID is unknown or not
// visible to actor, domain.ErrReturnRequestNotFound when requestID is
// unknown, belongs to a different item, or was not raised by actor (an
// ownership mismatch is masked the same way an invisible item is — see
// domain.ReturnRequestRepository.Cancel's own doc), or
// domain.ErrReturnRequestNotOpen when requestID has already been fulfilled
// or cancelled.
func (s *ReturnRequestService) Cancel(ctx context.Context, actor identity.Principal, itemID domain.ItemID, requestID domain.ReturnRequestID) error {
	it, err := s.items.Get(ctx, actor, itemID)
	if err != nil {
		return fmt.Errorf("app: cancel return request: %w", err)
	}

	err = s.uow.WithinReturnRequestTx(ctx, func(stores ReturnRequestTxStores) error {
		cancelled, txErr := stores.ReturnRequests.Cancel(ctx, actor.HouseholdID, requestID, actor.UserID)
		if txErr != nil {
			return txErr
		}
		if cancelled.ItemID != itemID {
			return domain.ErrReturnRequestNotFound
		}
		event := domain.NewItemEvent(domain.NewItemEventID(), itemID, it.Name, domain.EventReturnRequestCancelled, actor)
		if txErr = event.Validate(); txErr != nil {
			return txErr
		}
		return stores.Events.Append(ctx, &event)
	})
	if err != nil {
		return fmt.Errorf("app: cancel return request: %w", err)
	}

	s.logAction(ctx, "return request cancelled", itemID, requestID)
	return nil
}

// ListForItem returns itemID's return requests (every status), newest
// first, first confirming itemID is visible to viewer. Returns a wrapped
// domain.ErrItemNotFound when itemID is unknown or not visible to viewer; an
// empty slice, not an error, when the item has no requests.
func (s *ReturnRequestService) ListForItem(ctx context.Context, viewer identity.Principal, itemID domain.ItemID) ([]domain.ReturnRequest, error) {
	if _, err := s.items.Get(ctx, viewer, itemID); err != nil {
		return nil, fmt.Errorf("app: list return requests: %w", err)
	}
	reqs, err := s.requests.ListByItem(ctx, viewer.HouseholdID, itemID)
	if err != nil {
		return nil, fmt.Errorf("app: list return requests: %w", err)
	}
	return reqs, nil
}

// logAction writes one INFO-level audit line for a completed return-request
// mutation. It logs the item and request ids, never the requester's message
// — Nestorage's PII-out-of-logs convention (see ItemLinkService.logAction).
func (s *ReturnRequestService) logAction(ctx context.Context, msg string, itemID domain.ItemID, requestID domain.ReturnRequestID) {
	s.logger.InfoContext(ctx, "storage: "+msg, "item_id", itemID.String(), "return_request_id", requestID.String())
}
