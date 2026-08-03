package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// BinStore is the narrow port (ISP) BinTxStores.Bins exposes to a
// binTransactor's transactional closure: only the row-locked read and
// location-swap update BinMover.Move builds on — the bin-move analog of
// ItemStore (operations.go). Satisfied by a *domain.BinRepository
// constructed over a pgx transaction (a superset — see
// domain.BinRepository.GetForUpdate/Move for the exact contract this
// mirrors). Exported, unlike this file's other ports, because
// adapter.PostgresUnitOfWork must spell it by name to construct
// BinTxStores.
type BinStore interface {
	GetForUpdate(ctx context.Context, householdID identity.HouseholdID, id domain.BinID) (*domain.Bin, error)
	Move(ctx context.Context, householdID identity.HouseholdID, id domain.BinID, target domain.LocationID, now time.Time) (int64, error)
}

// BinItemIDLister is the narrow port (ISP) BinTxStores.Items exposes to fan
// out NSTR-41's moved events: every item currently sitting in the bin being
// relocated, unscoped by visibility — the audit record must not depend on
// what the mover's own visibility-scoped reads would show (see
// domain.ItemRepository.ListIDsByBin's own doc). It returns
// domain.ItemRef, not a bare domain.ItemID, despite the "IDs" name: each
// fanned-out EventMoved row needs an item_name snapshot (item_event's own
// NOT NULL column), and domain.ItemRepository.ListByBin — the method that
// already carries a name — is barred here because it is visibility-scoped.
// Satisfied by *domain.ItemRepository (a superset).
type BinItemIDLister interface {
	ListIDsByBin(ctx context.Context, householdID identity.HouseholdID, binID domain.BinID) ([]domain.ItemRef, error)
}

// BinTxStores bundles the tx-bound stores a binTransactor's closure
// operates on: BinStore for the location swap, BinItemIDLister to fan out
// one item_event row per item in the bin, EventAppender to append them. A
// struct bundle (NSTR-41's reconciliation R1), not positional parameters,
// mirroring OperationStores' own rationale (operations.go). Exported
// because adapter.PostgresUnitOfWork must spell it by name to satisfy
// binTransactor.
type BinTxStores struct {
	Bins   BinStore
	Items  BinItemIDLister
	Events EventAppender
}

// binTransactor runs fn inside one transaction against a BinTxStores
// bundle, committing when fn returns nil and rolling back — with no
// partial write, including any item_event rows fn appended — on any error
// fn returns, including a failed domain transition guard. The bin-move
// analog of transactor (operations.go): adapter.PostgresUnitOfWork
// supplies the real pgx transaction, with the bin row locked FOR UPDATE for
// its duration so a second, concurrent move of the same bin blocks rather
// than racing. Named for its single WithinBinTx method, per the same
// single-method-interface convention binFinder documents.
type binTransactor interface {
	WithinBinTx(ctx context.Context, fn func(BinTxStores) error) error
}

// locationFinder is the narrow port (ISP) BinMover depends on to confirm a
// move's target location is visible to the acting principal, satisfied by
// domain.LocationRepository (a superset, via FindVisibleByID). Named for its
// single method, mirroring binFinder's own naming rationale.
type locationFinder interface {
	FindVisibleByID(ctx context.Context, viewer identity.Principal, id domain.LocationID) (*domain.Location, error)
}

// MoveResult carries the facts a completed bin move leaves behind: which bin
// moved, where it moved from and to, who moved it, and when. Returned from
// every successful BinMover.Move — deliberately, per the ticket's own
// rationale, so the later item-history epic can record the move without
// this ticket taking a dependency on any unbuilt history package; this
// return value is the seam that epic wires into. NSTR-41 wires that seam
// itself: Move appends an EventMoved row per item, sourced from the same
// bin/from/to values MoveResult reports.
type MoveResult struct {
	BinID          domain.BinID
	FromLocationID domain.LocationID
	ToLocationID   domain.LocationID
	MovedBy        identity.UserID
	MovedAt        time.Time
}

// BinMover implements the single bin-relocation operation this bounded
// context exposes: Move. It runs inside one transaction with the bin row
// locked FOR UPDATE for its duration (see binTransactor), mirroring
// OperationService's own transactional shape: two concurrent moves of the
// same bin never race, because the loser blocks on the row lock, then
// re-reads the just-committed location and re-runs MoveTo's own no-op guard
// against it before persisting anything itself.
type BinMover struct {
	uow    binTransactor
	bins   binFinder
	locs   locationFinder
	clock  func() time.Time
	logger *slog.Logger
}

// NewBinMover constructs BinMover. Every dependency is required; a missing
// one panics at construction time, matching every other constructor in this
// codebase (see NewOperationService).
func NewBinMover(uow binTransactor, bins binFinder, locs locationFinder, clock func() time.Time, logger *slog.Logger) *BinMover {
	if uow == nil {
		panic("storage/app: NewBinMover requires a non-nil binTransactor")
	}
	if bins == nil {
		panic("storage/app: NewBinMover requires a non-nil binFinder")
	}
	if locs == nil {
		panic("storage/app: NewBinMover requires a non-nil locationFinder")
	}
	if clock == nil {
		panic("storage/app: NewBinMover requires a non-nil clock func")
	}
	if logger == nil {
		panic("storage/app: NewBinMover requires a non-nil logger")
	}
	return &BinMover{uow: uow, bins: bins, locs: locs, clock: clock, logger: logger}
}

// Move relocates binID to target on actor's behalf. Items are untouched by
// construction: they carry bin_id, not location_id, so they ride along
// implicitly with the bin they already sit in. In the same transaction as
// the location swap, Move appends one EventMoved row per item currently in
// the bin (NSTR-41's reconciliation R4: 'moved' means the bin's containing
// location changed, not the item's own bin — bin_id/bin_label on every
// fanned-out row stay the containing bin, unchanged by the move).
//
// Returns a wrapped domain.ErrBinNotFound when binID is unknown or not
// visible to actor; domain.ErrBinAlreadyInLocation (unwrapped by MoveTo, but
// wrapped here like every other error this method returns, so
// errors.Is still finds it) when target is binID's current location; or a
// wrapped domain.ErrLocationNotFound when target — or, defensively, the
// bin's own current location — is unknown or not visible to actor. The
// no-op guard runs before the target-visibility check: moving to the bin's
// own current location needs no further validation, since that location is
// already known to exist and be visible (the bin sits in it).
func (m *BinMover) Move(ctx context.Context, actor identity.Principal, binID domain.BinID, target domain.LocationID) (MoveResult, error) {
	bin, err := m.bins.FindVisibleByID(ctx, actor, binID)
	if err != nil {
		return MoveResult{}, fmt.Errorf("storage: move bin: %w", err)
	}

	from := bin.LocationID
	if err := bin.MoveTo(target); err != nil {
		return MoveResult{}, fmt.Errorf("storage: move bin: %w", err)
	}

	fromLoc, err := m.locs.FindVisibleByID(ctx, actor, from)
	if err != nil {
		return MoveResult{}, fmt.Errorf("storage: move bin: %w", err)
	}
	toLoc, err := m.locs.FindVisibleByID(ctx, actor, target)
	if err != nil {
		return MoveResult{}, fmt.Errorf("storage: move bin: %w", err)
	}

	now := m.clock()
	err = m.uow.WithinBinTx(ctx, func(stores BinTxStores) error {
		locked, txErr := stores.Bins.GetForUpdate(ctx, actor.HouseholdID, binID)
		if txErr != nil {
			return txErr
		}
		if txErr = locked.MoveTo(target); txErr != nil {
			return txErr
		}
		if _, txErr = stores.Bins.Move(ctx, actor.HouseholdID, binID, target, now); txErr != nil {
			return txErr
		}
		return m.appendMoveEvents(ctx, stores, actor, bin, from, fromLoc, target, toLoc)
	})
	if err != nil {
		return MoveResult{}, fmt.Errorf("storage: move bin: %w", err)
	}

	m.logAction(ctx, "bin moved", binID, "from_location_id", from.String(), "to_location_id", target.String())
	return MoveResult{
		BinID:          binID,
		FromLocationID: from,
		ToLocationID:   target,
		MovedBy:        actor.UserID,
		MovedAt:        now,
	}, nil
}

// appendMoveEvents fans out one EventMoved row per item currently in bin,
// inside the same transaction as the location swap (see binTransactor's own
// doc): NSTR-40's item_event_move_check requires all four location
// snapshot fields set on every 'moved' row, and the audit record must
// include every item in the bin, not only what the mover's own
// visibility-scoped reads would show (see BinItemIDLister's own doc).
func (m *BinMover) appendMoveEvents(ctx context.Context, stores BinTxStores, actor identity.Principal, bin *domain.Bin, from domain.LocationID, fromLoc *domain.Location, to domain.LocationID, toLoc *domain.Location) error {
	refs, err := stores.Items.ListIDsByBin(ctx, actor.HouseholdID, bin.ID)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		event := domain.NewItemEvent(domain.NewItemEventID(), ref.ID, ref.Name, domain.EventMoved, actor)
		event.BinID, event.BinLabel = &bin.ID, binLabel(bin)
		event.FromLocationID, event.FromLocationLabel = &from, fromLoc.Name
		event.ToLocationID, event.ToLocationLabel = &to, toLoc.Name
		if err := event.Validate(); err != nil {
			return err
		}
		if err := stores.Events.Append(ctx, &event); err != nil {
			return err
		}
	}
	return nil
}

// logAction writes one INFO-level audit line for a completed move, mirroring
// OperationService.logAction: the bin's id and both location ids, never
// anything naming the bin or a person — Nestorage's PII-out-of-logs
// convention.
func (m *BinMover) logAction(ctx context.Context, msg string, id domain.BinID, extra ...any) {
	args := append([]any{"bin_id", id.String()}, extra...)
	m.logger.InfoContext(ctx, "storage: "+msg, args...)
}
