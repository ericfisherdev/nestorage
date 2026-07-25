package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// ItemStore is the narrow port (ISP) OperationStores.Items exposes to a
// transactor's transactional closure: only the row-locked read and
// placement-swap update OperationService's add/remove/return build on.
// Satisfied by a *domain.ItemRepository constructed over a pgx transaction
// (a superset — see domain.ItemRepository.GetForUpdate/Move for the exact
// contract this mirrors). Exported, unlike the other ports in this file,
// because adapter.PostgresUnitOfWork must spell it by name to construct
// OperationStores.
type ItemStore interface {
	GetForUpdate(ctx context.Context, id domain.ItemID) (*domain.Item, error)
	Move(ctx context.Context, id domain.ItemID, dst domain.Placement) (int64, error)
}

// binFinder is the narrow port (ISP) OperationService depends on to
// resolve a destination bin the acting principal may actually see,
// satisfied by domain.BinRepository (a superset, via FindVisibleByID).
// Named for the single method it exposes, per Go's single-method-interface
// naming convention (io.Reader, fmt.Stringer, ...).
type binFinder interface {
	FindVisibleByID(ctx context.Context, viewer identity.Principal, id domain.BinID) (*domain.Bin, error)
}

// ReturnRequestFulfiller is the narrow port (ISP) OperationStores.ReturnRequests
// exposes to a transactor's transactional closure: only the
// held-to-bin-fulfilment method OperationService.transition needs (NSTR-43).
// Deliberately excludes Create/Cancel — those are ReturnRequestService's own,
// differently-shaped ReturnRequestLifecycleStore port (return_request.go),
// reached through a separate transaction. Satisfied by a
// *domain.ReturnRequestRepository (a superset) constructed over a pgx
// transaction. Exported, like ItemStore, because adapter.PostgresUnitOfWork
// must spell it by name to satisfy transactor.
type ReturnRequestFulfiller interface {
	FulfillOpenForItem(ctx context.Context, itemID domain.ItemID, at time.Time) ([]domain.ReturnRequest, error)
}

// userLabelResolver is the narrow port (ISP) OperationService depends on to
// resolve a fulfilled return request's requester into the display name
// ReturnRequestNotification.RequesterLabel carries, satisfied by
// identity.UserRepository (a superset, via FindByID). Named for the single
// method it exposes, mirroring binFinder's own naming rationale.
type userLabelResolver interface {
	FindByID(ctx context.Context, id identity.UserID) (*identity.User, error)
}

// OperationStores bundles the tx-bound stores a transactor's closure
// operates on: ItemStore for the placement swap, EventAppender for the
// item_event row that records it in the same transaction, and (NSTR-43)
// ReturnRequestFulfiller for flipping any open return request the placement
// swap satisfies. A struct bundle (NSTR-41's reconciliation R1), not
// positional parameters — open for extension, closed for modification.
// Exported because adapter.PostgresUnitOfWork must spell it by name to
// satisfy transactor.
type OperationStores struct {
	Items          ItemStore
	Events         EventAppender
	ReturnRequests ReturnRequestFulfiller
}

// transactor runs fn inside one transaction against an OperationStores
// bundle, committing when fn returns nil and rolling back — with no
// partial write, including the item_event row fn appended — on any error
// fn returns, including a failed domain transition guard. This is the seam
// that keeps OperationService pure and unit-testable with a fake;
// adapter.PostgresUnitOfWork supplies the real pgx transaction, with the
// item row locked FOR UPDATE for its duration so a second, concurrent
// operation against the same item blocks rather than racing. Named for its
// single WithinTx method (see binFinder's own doc for the same rationale);
// it plays the unit-of-work role the ticket describes under that name.
type transactor interface {
	WithinTx(ctx context.Context, fn func(OperationStores) error) error
}

// OperationVerb names which of OperationService's three placement
// transitions an Operation records.
type OperationVerb string

// The three verbs OperationService can perform.
const (
	OperationAdd    OperationVerb = "add"
	OperationRemove OperationVerb = "remove"
	OperationReturn OperationVerb = "return"
)

// String returns the verb's display value.
func (v OperationVerb) String() string { return string(v) }

// Operation records a completed add/remove/return: which verb ran, the item
// afterward, the bin acted on (nil for RemoveFromBin, which has no
// destination bin), the acting principal's display label and user id, and
// (NSTR-43) any open return requests the placement swap just fulfilled —
// nil/empty for every RemoveFromBin, and for an AddToBin/ReturnToBin that
// resolved no open requests. This is deliberately returned from every
// OperationService method — per the ticket's own rationale, this service is
// the single place these state transitions happen, so the later
// item-history epic can hook events from here without OperationService
// depending on any event/history package itself.
type Operation struct {
	Verb                    OperationVerb
	Item                    *domain.Item
	BinID                   *domain.BinID
	Actor                   string
	UserID                  identity.UserID
	FulfilledReturnRequests []domain.ReturnRequest
}

// OperationService implements the three item placement transitions this
// bounded context exposes: AddToBin, RemoveFromBin (check out), and
// ReturnToBin. Each runs inside one transaction with the item row locked
// FOR UPDATE for its duration (see transactor), so two concurrent
// operations against the same item never produce a partial write — the
// loser blocks on the row lock, then re-reads the just-committed state and
// fails its own domain transition guard (ErrItemAlreadyInBin/
// ErrItemAlreadyCheckedOut/ErrItemNotCheckedOut). NSTR-41 extends every
// transition to append the matching item_event row (added/removed/
// returned) inside that same transaction, so the placement change and its
// audit record commit or roll back together. NSTR-43 further extends
// AddToBin/ReturnToBin (any held-to-bin transition) to flip every open
// return request on the item to fulfilled inside that same transaction, and
// to notify the fulfilled requesters best-effort once it commits.
type OperationService struct {
	uow      transactor
	bins     binFinder
	users    userLabelResolver
	notifier ReturnRequestNotifier
	logger   *slog.Logger
}

// NewOperationService constructs OperationService. Every dependency is
// required; a missing one panics at construction time, matching every other
// constructor in this codebase (see NewItemService).
func NewOperationService(uow transactor, bins binFinder, users userLabelResolver, notifier ReturnRequestNotifier, logger *slog.Logger) *OperationService {
	if uow == nil {
		panic("storage/app: NewOperationService requires a non-nil transactor")
	}
	if bins == nil {
		panic("storage/app: NewOperationService requires a non-nil binFinder")
	}
	if users == nil {
		panic("storage/app: NewOperationService requires a non-nil userLabelResolver")
	}
	if notifier == nil {
		panic("storage/app: NewOperationService requires a non-nil ReturnRequestNotifier")
	}
	if logger == nil {
		panic("storage/app: NewOperationService requires a non-nil logger")
	}
	return &OperationService{uow: uow, bins: bins, users: users, notifier: notifier, logger: logger}
}

// AddToBin places itemID into binID on actor's behalf, clearing any holder,
// and appends an EventAdded row attributed to actor in the same
// transaction. Returns a wrapped domain.ErrBinNotFound when binID is
// unknown or not visible to actor, a wrapped domain.ErrItemNotFound when
// itemID is unknown, or a wrapped domain.ErrItemAlreadyInBin when itemID is
// already sitting in a bin. AddToBin carries no operation note (see R16 of
// NSTR-41's own reconciliation: note is check-out/return only).
func (s *OperationService) AddToBin(ctx context.Context, actor identity.Principal, itemID domain.ItemID, binID domain.BinID) (Operation, error) {
	bin, err := s.bins.FindVisibleByID(ctx, actor, binID)
	if err != nil {
		return Operation{}, fmt.Errorf("storage: add to bin: %w", err)
	}

	it, fulfilled, err := s.transition(ctx, itemID,
		func(it *domain.Item) error { return it.EnterBin(bin.ID) },
		domain.PlacementInBin(bin.ID),
		func(_ context.Context, _ placementSnapshot, it *domain.Item) (domain.ItemEvent, error) {
			e := domain.NewItemEvent(domain.NewItemEventID(), it.ID, it.Name, domain.EventAdded, actor)
			e.BinID, e.BinLabel = &bin.ID, binLabel(bin)
			return e, nil
		},
	)
	if err != nil {
		return Operation{}, fmt.Errorf("storage: add to bin: %w", err)
	}

	s.logAction(ctx, "item added to bin", itemID, "bin_id", bin.ID.String())
	return Operation{Verb: OperationAdd, Item: it, BinID: &bin.ID, Actor: actor.Actor(), UserID: actor.UserID, FulfilledReturnRequests: fulfilled}, nil
}

// RemoveFromBin checks itemID out to actor, clearing its current bin, and
// appends an EventRemoved row attributed to actor in the same transaction,
// carrying note and a snapshot of the bin the item just left. Returns
// domain.ErrHolderRequired (unwrapped, a precondition checked before any
// store access) when actor is not a real person (identity.KindUser) — the
// account api key integration principal has no person behind it to
// attribute the hold to — a wrapped domain.ErrItemNotFound when itemID is
// unknown, or a wrapped domain.ErrItemAlreadyCheckedOut when itemID is
// already held. RemoveFromBin's transition destination is a holder, never a
// bin, so it can never fulfil a return request (NSTR-43's own held-to-bin
// rule) — the fulfilled slice transition returns is discarded here.
func (s *OperationService) RemoveFromBin(ctx context.Context, actor identity.Principal, itemID domain.ItemID, note *string) (Operation, error) {
	if actor.Kind != identity.KindUser {
		return Operation{}, domain.ErrHolderRequired
	}

	it, _, err := s.transition(ctx, itemID,
		func(it *domain.Item) error { return it.CheckOut(actor.UserID) },
		domain.PlacementHeldBy(actor.UserID),
		func(ctx context.Context, before placementSnapshot, it *domain.Item) (domain.ItemEvent, error) {
			return s.buildRemovedEvent(ctx, actor, before, it, note)
		},
	)
	if err != nil {
		return Operation{}, fmt.Errorf("storage: remove from bin: %w", err)
	}

	s.logAction(ctx, "item removed from bin", itemID)
	return Operation{Verb: OperationRemove, Item: it, Actor: actor.Actor(), UserID: actor.UserID}, nil
}

// buildRemovedEvent builds RemoveFromBin's EventRemoved row from before —
// the pre-guard snapshot transition took immediately after GetForUpdate and
// before CheckOut cleared CurrentBinID in memory (NSTR-41's reconciliation
// R5: reading it after the guard runs would find it already nil).
// before.BinID is guaranteed set here: CheckOut's own guard already
// rejected an item that was not sitting in a bin (ErrItemAlreadyCheckedOut)
// before buildEvent is ever reached. The bin's own Code/Name (for BinLabel)
// are looked up via the same binFinder AddToBin/ReturnToBin already use for
// their destination bin — safe to call mid-transaction since it is not
// tx-scoped, and the actor's visibility into the source bin was already
// established by the prior item-detail read that let them reach this
// operation at all.
func (s *OperationService) buildRemovedEvent(ctx context.Context, actor identity.Principal, before placementSnapshot, it *domain.Item, note *string) (domain.ItemEvent, error) {
	if before.BinID == nil {
		return domain.ItemEvent{}, fmt.Errorf("storage: remove from bin: item %s had no bin to snapshot before check-out", it.ID)
	}
	bin, err := s.bins.FindVisibleByID(ctx, actor, *before.BinID)
	if err != nil {
		return domain.ItemEvent{}, err
	}
	e := domain.NewItemEvent(domain.NewItemEventID(), it.ID, it.Name, domain.EventRemoved, actor)
	e.BinID, e.BinLabel = before.BinID, binLabel(bin)
	e.Note = noteValue(note)
	return e, nil
}

// ReturnToBin places itemID — currently held — back into binID on actor's
// behalf, clearing its holder, and appends an EventReturned row attributed
// to actor in the same transaction, carrying note. Returns a wrapped
// domain.ErrBinNotFound when binID is unknown or not visible to actor, a
// wrapped domain.ErrItemNotFound when itemID is unknown, or a wrapped
// domain.ErrItemNotCheckedOut when itemID is not currently held.
func (s *OperationService) ReturnToBin(ctx context.Context, actor identity.Principal, itemID domain.ItemID, binID domain.BinID, note *string) (Operation, error) {
	bin, err := s.bins.FindVisibleByID(ctx, actor, binID)
	if err != nil {
		return Operation{}, fmt.Errorf("storage: return to bin: %w", err)
	}

	it, fulfilled, err := s.transition(ctx, itemID,
		func(it *domain.Item) error { return it.ReturnTo(bin.ID) },
		domain.PlacementInBin(bin.ID),
		func(_ context.Context, _ placementSnapshot, it *domain.Item) (domain.ItemEvent, error) {
			e := domain.NewItemEvent(domain.NewItemEventID(), it.ID, it.Name, domain.EventReturned, actor)
			e.BinID, e.BinLabel = &bin.ID, binLabel(bin)
			e.Note = noteValue(note)
			return e, nil
		},
	)
	if err != nil {
		return Operation{}, fmt.Errorf("storage: return to bin: %w", err)
	}

	s.logAction(ctx, "item returned to bin", itemID, "bin_id", bin.ID.String())
	return Operation{Verb: OperationReturn, Item: it, BinID: &bin.ID, Actor: actor.Actor(), UserID: actor.UserID, FulfilledReturnRequests: fulfilled}, nil
}

// placementSnapshot captures an item's placement fields before a domain
// transition guard mutates them in memory — EnterBin/CheckOut/ReturnTo all
// reassign CurrentBinID/HeldBy on the very *domain.Item transition reads,
// so a caller that needs the PRE-transition bin (RemoveFromBin's removed
// event) must read it before the guard runs, not after (NSTR-41's
// reconciliation R5). Captured once per transition and handed to every
// buildEvent closure. R5 also flags this as the same snapshot NSTR-43 reads
// for its own guard-time "was this item held" question (before.HeldBy) — it
// is taken exactly once per transition, never a second time for
// fulfilment's sake.
type placementSnapshot struct {
	BinID  *domain.BinID
	HeldBy *identity.UserID
}

// snapshotPlacement copies it's current placement fields, mirroring
// domain.Placement's own BinID/HeldBy shape.
func snapshotPlacement(it *domain.Item) placementSnapshot {
	return placementSnapshot{BinID: it.CurrentBinID, HeldBy: it.HeldBy}
}

// transition runs the shared shape all three operations share inside one
// transaction: lock itemID FOR UPDATE, snapshot its pre-guard placement
// (see placementSnapshot), apply guard (the domain transition method —
// EnterBin/CheckOut/ReturnTo — which validates the current state in memory
// and leaves the item unmodified on a guard failure), persist the swap to
// dst via ItemStore.Move (advancing placement_changed_at in the same
// statement, see Move's own doc), re-read the row once more (cheap: the
// lock is already held) so the *domain.Item this returns carries the
// DB-persisted placement_changed_at/updated_at, then build and append the
// matching item_event row via buildEvent — all inside the same
// transaction, so the placement change and its event commit or roll back
// together (NSTR-41's atomicity contract: no outbox, one pgx.Tx). Factored
// out so AddToBin/RemoveFromBin/ReturnToBin differ only in which guard,
// destination placement, and event builder they supply.
//
// NSTR-43: when dst names a bin AND the item was held before the guard ran
// (before.HeldBy != nil — true for AddToBin and ReturnToBin, the only two
// callers whose dst ever names a bin; never for RemoveFromBin), transition
// also flips every open return request on itemID to fulfilled, in the same
// transaction, via OperationStores.ReturnRequests.FulfillOpenForItem — the
// held-to-bin rule the ticket calls for, reusing the placementSnapshot
// RemoveFromBin's own removed-event already needs rather than taking a
// second one. On successful commit, it fans the fulfilled requests out to
// ReturnRequestNotifier.ReturnRequestsFulfilled post-commit and
// best-effort (see notifyFulfilled's own doc) before returning them to the
// caller for Operation.FulfilledReturnRequests.
func (s *OperationService) transition(
	ctx context.Context,
	itemID domain.ItemID,
	guard func(*domain.Item) error,
	dst domain.Placement,
	buildEvent func(ctx context.Context, before placementSnapshot, it *domain.Item) (domain.ItemEvent, error),
) (*domain.Item, []domain.ReturnRequest, error) {
	var it *domain.Item
	var fulfilled []domain.ReturnRequest
	err := s.uow.WithinTx(ctx, func(stores OperationStores) error {
		var txErr error
		it, txErr = stores.Items.GetForUpdate(ctx, itemID)
		if txErr != nil {
			return txErr
		}
		before := snapshotPlacement(it)
		if txErr = guard(it); txErr != nil {
			return txErr
		}
		if _, txErr = stores.Items.Move(ctx, itemID, dst); txErr != nil {
			return txErr
		}
		it, txErr = stores.Items.GetForUpdate(ctx, itemID)
		if txErr != nil {
			return txErr
		}

		event, txErr := buildEvent(ctx, before, it)
		if txErr != nil {
			return txErr
		}
		if txErr = event.Validate(); txErr != nil {
			return txErr
		}
		if txErr = stores.Events.Append(ctx, &event); txErr != nil {
			return txErr
		}

		if dst.BinID != nil && before.HeldBy != nil {
			fulfilled, txErr = stores.ReturnRequests.FulfillOpenForItem(ctx, itemID, it.PlacementChangedAt)
			if txErr != nil {
				return txErr
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	s.notifyFulfilled(ctx, it, fulfilled)
	return it, fulfilled, nil
}

// notifyFulfilled tells ReturnRequestNotifier about every request fulfilled
// fanned out of a just-committed transition, post-commit and
// best-effort — a resolution failure here must never fail (nor, since the
// transaction already committed, be capable of rolling back) the placement
// operation that already succeeded. Does nothing when fulfilled is empty,
// which is the common case (RemoveFromBin, and any AddToBin/ReturnToBin
// with no open requests on the item).
func (s *OperationService) notifyFulfilled(ctx context.Context, it *domain.Item, fulfilled []domain.ReturnRequest) {
	if len(fulfilled) == 0 {
		return
	}
	notifications := make([]ReturnRequestNotification, 0, len(fulfilled))
	for _, req := range fulfilled {
		notifications = append(notifications, s.buildFulfilledNotification(ctx, it, req))
	}
	s.notifier.ReturnRequestsFulfilled(ctx, notifications)
}

// buildFulfilledNotification projects one fulfilled domain.ReturnRequest
// into a ReturnRequestNotification, resolving the requester's display name
// via userLabelResolver — the requester is a different person from actor
// (the one who just performed the placement change) for every notification
// built here, so unlike ReturnRequestService.Request (which already has its
// own actor's label at hand), this is the one call site that needs a live
// identity lookup. A resolution failure is logged and leaves
// RequesterLabel "" rather than aborting the whole notify fan-out — one
// requester's name being unavailable must not silence every other
// requester's notification.
func (s *OperationService) buildFulfilledNotification(ctx context.Context, it *domain.Item, req domain.ReturnRequest) ReturnRequestNotification {
	label := ""
	if user, err := s.users.FindByID(ctx, req.RequesterID); err != nil {
		s.logger.WarnContext(ctx, "storage: return request: resolve requester label", "return_request_id", req.ID.String(), "error", err)
	} else {
		label = user.DisplayName
	}
	return ReturnRequestNotification{
		RequestID: req.ID, ItemID: it.ID, ItemName: it.Name,
		RequesterID: req.RequesterID, RequesterLabel: label,
		HolderID: req.HolderID, Message: req.Message,
		CreatedAt: req.CreatedAt, ResolvedAt: req.ResolvedAt,
	}
}

// logAction writes one INFO-level audit line for a completed operation,
// mirroring ItemService.logAction: the item's id (and, when relevant, the
// bin's), never its name or description — Nestorage's PII-out-of-logs
// convention.
func (s *OperationService) logAction(ctx context.Context, msg string, id domain.ItemID, extra ...any) {
	args := append([]any{"item_id", id.String()}, extra...)
	s.logger.InfoContext(ctx, "storage: "+msg, args...)
}
