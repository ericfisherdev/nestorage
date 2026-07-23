package app

import (
	"context"
	"fmt"
	"log/slog"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// ItemStore is the narrow port (ISP) a unitOfWork's transactional closure
// receives: only the row-locked read and placement-swap update
// OperationService's add/remove/return build on. Satisfied by a
// *domain.ItemRepository constructed over a pgx transaction (a superset —
// see domain.ItemRepository.GetForUpdate/Move for the exact contract this
// mirrors). Exported, unlike the other ports in this file, because
// adapter.PostgresUnitOfWork's WithinTx method must spell this type by name
// to satisfy unitOfWork's identical method signature.
type ItemStore interface {
	GetForUpdate(ctx context.Context, id domain.ItemID) (*domain.Item, error)
	Move(ctx context.Context, id domain.ItemID, dst domain.Placement) (int64, error)
}

// binVisibility is the narrow port (ISP) OperationService depends on to
// resolve a destination bin the acting principal may actually see,
// satisfied by domain.BinRepository (a superset, via FindVisibleByID).
type binVisibility interface {
	FindVisibleByID(ctx context.Context, viewer identity.Principal, id domain.BinID) (*domain.Bin, error)
}

// unitOfWork runs fn inside one transaction against a tx-bound ItemStore,
// committing when fn returns nil and rolling back — with no partial write —
// on any error fn returns, including a failed domain transition guard. This
// is the seam that keeps OperationService pure and unit-testable with a
// fake; adapter.PostgresUnitOfWork supplies the real pgx transaction, with
// the item row locked FOR UPDATE for its duration so a second, concurrent
// operation against the same item blocks rather than racing.
type unitOfWork interface {
	WithinTx(ctx context.Context, fn func(ItemStore) error) error
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
// destination bin), and the acting principal's display label and user id.
// This is deliberately returned from every OperationService method — per
// the ticket's own rationale, this service is the single place these state
// transitions happen, so the later item-history epic can hook events from
// here without OperationService depending on any event/history package
// itself.
type Operation struct {
	Verb   OperationVerb
	Item   *domain.Item
	BinID  *domain.BinID
	Actor  string
	UserID identity.UserID
}

// OperationService implements the three item placement transitions this
// bounded context exposes: AddToBin, RemoveFromBin (check out), and
// ReturnToBin. Each runs inside one transaction with the item row locked
// FOR UPDATE for its duration (see unitOfWork), so two concurrent
// operations against the same item never produce a partial write — the
// loser blocks on the row lock, then re-reads the just-committed state and
// fails its own domain transition guard (ErrItemAlreadyInBin/
// ErrItemAlreadyCheckedOut/ErrItemNotCheckedOut).
type OperationService struct {
	uow    unitOfWork
	bins   binVisibility
	logger *slog.Logger
}

// NewOperationService constructs OperationService. Every dependency is
// required; a missing one panics at construction time, matching every other
// constructor in this codebase (see NewItemService).
func NewOperationService(uow unitOfWork, bins binVisibility, logger *slog.Logger) *OperationService {
	if uow == nil {
		panic("storage/app: NewOperationService requires a non-nil unitOfWork")
	}
	if bins == nil {
		panic("storage/app: NewOperationService requires a non-nil binVisibility")
	}
	if logger == nil {
		panic("storage/app: NewOperationService requires a non-nil logger")
	}
	return &OperationService{uow: uow, bins: bins, logger: logger}
}

// AddToBin places itemID into binID on actor's behalf, clearing any holder.
// Returns a wrapped domain.ErrBinNotFound when binID is unknown or not
// visible to actor, a wrapped domain.ErrItemNotFound when itemID is
// unknown, or a wrapped domain.ErrItemAlreadyInBin when itemID is already
// sitting in a bin.
func (s *OperationService) AddToBin(ctx context.Context, actor identity.Principal, itemID domain.ItemID, binID domain.BinID) (Operation, error) {
	bin, err := s.bins.FindVisibleByID(ctx, actor, binID)
	if err != nil {
		return Operation{}, fmt.Errorf("storage: add to bin: %w", err)
	}

	it, err := s.transition(ctx, itemID, func(it *domain.Item) error { return it.EnterBin(bin.ID) }, domain.PlacementInBin(bin.ID))
	if err != nil {
		return Operation{}, fmt.Errorf("storage: add to bin: %w", err)
	}

	s.logAction(ctx, "item added to bin", itemID, "bin_id", bin.ID.String())
	return Operation{Verb: OperationAdd, Item: it, BinID: &bin.ID, Actor: actor.Actor(), UserID: actor.UserID}, nil
}

// RemoveFromBin checks itemID out to actor, clearing its current bin.
// Returns domain.ErrHolderRequired (unwrapped, a precondition checked
// before any store access) when actor is not a real person
// (identity.KindUser) — the account api key integration principal has no
// person behind it to attribute the hold to — a wrapped
// domain.ErrItemNotFound when itemID is unknown, or a wrapped
// domain.ErrItemAlreadyCheckedOut when itemID is already held.
func (s *OperationService) RemoveFromBin(ctx context.Context, actor identity.Principal, itemID domain.ItemID) (Operation, error) {
	if actor.Kind != identity.KindUser {
		return Operation{}, domain.ErrHolderRequired
	}

	it, err := s.transition(ctx, itemID, func(it *domain.Item) error { return it.CheckOut(actor.UserID) }, domain.PlacementHeldBy(actor.UserID))
	if err != nil {
		return Operation{}, fmt.Errorf("storage: remove from bin: %w", err)
	}

	s.logAction(ctx, "item removed from bin", itemID)
	return Operation{Verb: OperationRemove, Item: it, Actor: actor.Actor(), UserID: actor.UserID}, nil
}

// ReturnToBin places itemID — currently held — back into binID on actor's
// behalf, clearing its holder. Returns a wrapped domain.ErrBinNotFound when
// binID is unknown or not visible to actor, a wrapped
// domain.ErrItemNotFound when itemID is unknown, or a wrapped
// domain.ErrItemNotCheckedOut when itemID is not currently held.
func (s *OperationService) ReturnToBin(ctx context.Context, actor identity.Principal, itemID domain.ItemID, binID domain.BinID) (Operation, error) {
	bin, err := s.bins.FindVisibleByID(ctx, actor, binID)
	if err != nil {
		return Operation{}, fmt.Errorf("storage: return to bin: %w", err)
	}

	it, err := s.transition(ctx, itemID, func(it *domain.Item) error { return it.ReturnTo(bin.ID) }, domain.PlacementInBin(bin.ID))
	if err != nil {
		return Operation{}, fmt.Errorf("storage: return to bin: %w", err)
	}

	s.logAction(ctx, "item returned to bin", itemID, "bin_id", bin.ID.String())
	return Operation{Verb: OperationReturn, Item: it, BinID: &bin.ID, Actor: actor.Actor(), UserID: actor.UserID}, nil
}

// transition runs the shared shape all three operations share inside one
// transaction: lock itemID FOR UPDATE, apply guard (the domain transition
// method — EnterBin/CheckOut/ReturnTo — which validates the current state
// in memory and leaves the item unmodified on a guard failure), persist the
// swap to dst via ItemStore.Move (advancing placement_changed_at in the
// same statement, see Move's own doc), then re-read the row once more
// (cheap: the lock is already held) so the *domain.Item this returns
// carries the DB-persisted placement_changed_at/updated_at rather than the
// pre-transition values guard mutated in memory. Factored out so
// AddToBin/RemoveFromBin/ReturnToBin differ only in which guard and
// destination placement they supply.
func (s *OperationService) transition(ctx context.Context, itemID domain.ItemID, guard func(*domain.Item) error, dst domain.Placement) (*domain.Item, error) {
	var it *domain.Item
	err := s.uow.WithinTx(ctx, func(items ItemStore) error {
		var txErr error
		it, txErr = items.GetForUpdate(ctx, itemID)
		if txErr != nil {
			return txErr
		}
		if txErr = guard(it); txErr != nil {
			return txErr
		}
		if _, txErr = items.Move(ctx, itemID, dst); txErr != nil {
			return txErr
		}
		it, txErr = items.GetForUpdate(ctx, itemID)
		return txErr
	})
	if err != nil {
		return nil, err
	}
	return it, nil
}

// logAction writes one INFO-level audit line for a completed operation,
// mirroring ItemService.logAction: the item's id (and, when relevant, the
// bin's), never its name or description — Nestorage's PII-out-of-logs
// convention.
func (s *OperationService) logAction(ctx context.Context, msg string, id domain.ItemID, extra ...any) {
	args := append([]any{"item_id", id.String()}, extra...)
	s.logger.InfoContext(ctx, "storage: "+msg, args...)
}
