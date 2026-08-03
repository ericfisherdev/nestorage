package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// fakeBinStore is an in-memory app.BinStore fake standing in for the tx-bound
// BinRepository a real binTransactor constructs, mirroring fakeItemStore's
// own doc: GetForUpdate always returns a copy of the stored bin, so a
// caller's domain transition mutation (MoveTo, which mutates the *domain.Bin
// it is given) can never corrupt the fake's own state ahead of Move.
type fakeBinStore struct {
	bin             *domain.Bin
	getForUpdateErr error
	moveErr         error
	moveCalls       int

	// lastGetForUpdateHouseholdID/lastMoveHouseholdID record the householdID
	// BinMover actually passed through — NSTR-131 threads actor.HouseholdID
	// into both calls so the tx-bound store's own household predicate has
	// something to bind to; recording it here (rather than silently
	// accepting-and-ignoring it) is what lets
	// TestBinMover_Move_Success/TestBinMoveEmitsMovedEventPerItem catch a
	// regression that dropped or zeroed that argument before it ever reaches
	// a real BinRepository.
	lastGetForUpdateHouseholdID identity.HouseholdID
	lastMoveHouseholdID         identity.HouseholdID
}

func (f *fakeBinStore) GetForUpdate(_ context.Context, householdID identity.HouseholdID, id domain.BinID) (*domain.Bin, error) {
	f.lastGetForUpdateHouseholdID = householdID
	if f.getForUpdateErr != nil {
		return nil, f.getForUpdateErr
	}
	if f.bin == nil || f.bin.ID != id {
		return nil, domain.ErrBinNotFound
	}
	cp := *f.bin
	return &cp, nil
}

func (f *fakeBinStore) Move(_ context.Context, householdID identity.HouseholdID, id domain.BinID, target domain.LocationID, now time.Time) (int64, error) {
	f.moveCalls++
	f.lastMoveHouseholdID = householdID
	if f.moveErr != nil {
		return 0, f.moveErr
	}
	if f.bin == nil || f.bin.ID != id {
		return 0, domain.ErrBinNotFound
	}
	f.bin.LocationID = target
	f.bin.UpdatedAt = now
	return 1, nil
}

// fakeBinItemIDLister is an in-memory app.BinItemIDLister fake: ListIDsByBin
// always returns refs (or listErr, when set), standing in for the real
// ItemRepository.ListIDsByBin the moved-event fan-out reads.
type fakeBinItemIDLister struct {
	refs    []domain.ItemRef
	listErr error

	// lastHouseholdID records the householdID BinMover's moved-event fan-out
	// actually passed — see fakeBinStore's identical rationale above.
	lastHouseholdID identity.HouseholdID
}

func (f *fakeBinItemIDLister) ListIDsByBin(_ context.Context, householdID identity.HouseholdID, _ domain.BinID) ([]domain.ItemRef, error) {
	f.lastHouseholdID = householdID
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.refs, nil
}

// fakeBinUnitOfWork runs fn directly against its fakeBinStore/
// fakeBinItemIDLister/fakeEventAppender trio with no real transactional
// isolation, except for one thing it does simulate: on a fn error, it
// restores the fakeBinStore's bin to what it was before fn ran, mirroring
// fakeUnitOfWork's own rollback simulation (operations_test.go) so
// TestBinMoveEventFailureAbortsMove can assert the location swap was
// undone too.
type fakeBinUnitOfWork struct {
	store  *fakeBinStore
	items  *fakeBinItemIDLister
	events *fakeEventAppender
}

func (u *fakeBinUnitOfWork) WithinBinTx(_ context.Context, fn func(app.BinTxStores) error) error {
	before := *u.store.bin
	err := fn(app.BinTxStores{Bins: u.store, Items: u.items, Events: u.events})
	if err != nil {
		*u.store.bin = before
	}
	return err
}

// fakeBinVisibility (the binFinder fake BinMover shares with
// OperationService) is defined once, in operations_test.go.

// fakeLocationVisibility is an in-memory locationFinder fake keyed by
// location id: FindVisibleByID resolves any location previously registered
// (via the constructor or add), else notFoundErr. Keyed, not a single
// bin/notFoundErr pair like fakeBinVisibility, because BinMover.Move now
// resolves TWO distinct locations (from and to) through the same finder —
// see Move's own doc for why the source location's name is looked up too.
type fakeLocationVisibility struct {
	locs        map[domain.LocationID]domain.Location
	notFoundErr error
}

func (f *fakeLocationVisibility) FindVisibleByID(_ context.Context, _ identity.Principal, id domain.LocationID) (*domain.Location, error) {
	if loc, ok := f.locs[id]; ok {
		cp := loc
		return &cp, nil
	}
	return nil, f.notFoundErr
}

// add registers loc so a later FindVisibleByID(loc.ID) resolves it.
func (f *fakeLocationVisibility) add(loc domain.Location) {
	if f.locs == nil {
		f.locs = make(map[domain.LocationID]domain.Location)
	}
	f.locs[loc.ID] = loc
}

// movableFixture returns a fakeBinStore/fakeBinVisibility/
// fakeLocationVisibility trio around one bin sitting in from, for tests
// that move it to some other location. from is pre-registered on the
// returned fakeLocationVisibility (Move's own FromLocationLabel lookup
// needs it resolvable); the caller registers the target location itself
// via locs.add before calling Move.
func movableFixture(from domain.LocationID) (*fakeBinStore, *fakeBinVisibility, *fakeLocationVisibility, domain.BinID) {
	binID := domain.NewBinID()
	bin := &domain.Bin{ID: binID, Code: "A1", Name: "Bin A", LocationID: from}
	locs := &fakeLocationVisibility{}
	locs.add(domain.Location{ID: from, Name: "Garage"})
	return &fakeBinStore{bin: bin}, &fakeBinVisibility{bin: bin}, locs, binID
}

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// newTestBinMover wires a BinMover over store/bins/locs and fresh
// fakeBinItemIDLister/fakeEventAppender fakes (no items by default),
// returning the event appender alongside the mover so a test that cares
// which events were appended can inspect it directly.
func newTestBinMover(store *fakeBinStore, bins *fakeBinVisibility, locs *fakeLocationVisibility) (*app.BinMover, *fakeEventAppender) {
	events := &fakeEventAppender{}
	uow := &fakeBinUnitOfWork{store: store, items: &fakeBinItemIDLister{}, events: events}
	return app.NewBinMover(uow, bins, locs, fixedClock(time.Now()), testLogger()), events
}

func TestNewBinMover_PanicsOnNilDeps(t *testing.T) {
	store, bins, locs, _ := movableFixture(domain.NewLocationID())
	uow := &fakeBinUnitOfWork{store: store, items: &fakeBinItemIDLister{}, events: &fakeEventAppender{}}
	clock := fixedClock(time.Now())

	tests := []struct {
		name  string
		build func()
	}{
		{"nil transactor", func() { app.NewBinMover(nil, bins, locs, clock, testLogger()) }},
		{"nil binFinder", func() { app.NewBinMover(uow, nil, locs, clock, testLogger()) }},
		{"nil locationFinder", func() { app.NewBinMover(uow, bins, nil, clock, testLogger()) }},
		{"nil clock", func() { app.NewBinMover(uow, bins, locs, nil, testLogger()) }},
		{"nil logger", func() { app.NewBinMover(uow, bins, locs, clock, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("NewBinMover did not panic")
				}
			}()
			tt.build()
		})
	}
}

func TestBinMover_Move_Success(t *testing.T) {
	from := domain.NewLocationID()
	to := domain.NewLocationID()
	store, bins, locs, binID := movableFixture(from)
	locs.add(domain.Location{ID: to, Name: "Attic"})
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	uow := &fakeBinUnitOfWork{store: store, items: &fakeBinItemIDLister{}, events: &fakeEventAppender{}}
	mover := app.NewBinMover(uow, bins, locs, fixedClock(now), testLogger())

	holder := identity.NewUserID()
	actor := identity.NewUserPrincipal(holder, identity.RoleAdult, "Alice")
	actor.HouseholdID = identity.NewHouseholdID()

	result, err := mover.Move(context.Background(), actor, binID, to)
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if result.BinID != binID {
		t.Errorf("MoveResult.BinID = %v, want %v", result.BinID, binID)
	}
	if result.FromLocationID != from {
		t.Errorf("MoveResult.FromLocationID = %v, want %v", result.FromLocationID, from)
	}
	if result.ToLocationID != to {
		t.Errorf("MoveResult.ToLocationID = %v, want %v", result.ToLocationID, to)
	}
	if result.MovedBy != holder {
		t.Errorf("MoveResult.MovedBy = %v, want %v", result.MovedBy, holder)
	}
	if !result.MovedAt.Equal(now) {
		t.Errorf("MoveResult.MovedAt = %v, want %v", result.MovedAt, now)
	}
	if store.bin.LocationID != to {
		t.Errorf("persisted LocationID = %v, want %v", store.bin.LocationID, to)
	}
	if store.moveCalls != 1 {
		t.Errorf("Move called %d times, want exactly 1", store.moveCalls)
	}
	if store.lastGetForUpdateHouseholdID != actor.HouseholdID {
		t.Errorf("GetForUpdate's householdID = %v, want actor's own %v", store.lastGetForUpdateHouseholdID, actor.HouseholdID)
	}
	if store.lastMoveHouseholdID != actor.HouseholdID {
		t.Errorf("Move's householdID = %v, want actor's own %v", store.lastMoveHouseholdID, actor.HouseholdID)
	}
}

// TestBinMoveEmitsMovedEventPerItem proves NSTR-41's fan-out contract:
// moving a bin holding N items appends exactly N EventMoved rows, each
// naming its own item, the containing bin unchanged by the move (R4 of
// NSTR-41's own reconciliation), and the from/to location snapshot.
func TestBinMoveEmitsMovedEventPerItem(t *testing.T) {
	from := domain.NewLocationID()
	to := domain.NewLocationID()
	store, bins, locs, binID := movableFixture(from)
	locs.add(domain.Location{ID: to, Name: "Attic"})
	itemA := domain.ItemRef{ID: domain.NewItemID(), Name: "Stove"}
	itemB := domain.ItemRef{ID: domain.NewItemID(), Name: "Lantern"}
	events := &fakeEventAppender{}
	items := &fakeBinItemIDLister{refs: []domain.ItemRef{itemA, itemB}}
	uow := &fakeBinUnitOfWork{store: store, items: items, events: events}
	mover := app.NewBinMover(uow, bins, locs, fixedClock(time.Now()), testLogger())
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Alice")
	actor.HouseholdID = identity.NewHouseholdID()

	if _, err := mover.Move(context.Background(), actor, binID, to); err != nil {
		t.Fatalf("Move: %v", err)
	}

	if len(events.events) != 2 {
		t.Fatalf("Move appended %d events, want exactly 2 (one per item)", len(events.events))
	}
	if items.lastHouseholdID != actor.HouseholdID {
		t.Errorf("ListIDsByBin's householdID = %v, want actor's own %v", items.lastHouseholdID, actor.HouseholdID)
	}
	for i, want := range []domain.ItemRef{itemA, itemB} {
		got := events.events[i]
		if got.Kind != domain.EventMoved {
			t.Errorf("event[%d].Kind = %v, want %v", i, got.Kind, domain.EventMoved)
		}
		if got.ItemID != want.ID || got.ItemName != want.Name {
			t.Errorf("event[%d] item = %v/%q, want %v/%q", i, got.ItemID, got.ItemName, want.ID, want.Name)
		}
		if got.BinID == nil || *got.BinID != binID {
			t.Errorf("event[%d].BinID = %v, want the containing bin %v (unchanged by the move)", i, got.BinID, binID)
		}
		if got.FromLocationID == nil || *got.FromLocationID != from {
			t.Errorf("event[%d].FromLocationID = %v, want %v", i, got.FromLocationID, from)
		}
		if got.ToLocationID == nil || *got.ToLocationID != to {
			t.Errorf("event[%d].ToLocationID = %v, want %v", i, got.ToLocationID, to)
		}
	}
}

// TestBinMoveEventFailureAbortsMove proves atomicity for the fan-out path:
// a failing EventAppender aborts the whole move, rolling the bin's
// location back too — no relocated bin left with a missing event.
func TestBinMoveEventFailureAbortsMove(t *testing.T) {
	from := domain.NewLocationID()
	to := domain.NewLocationID()
	store, bins, locs, binID := movableFixture(from)
	locs.add(domain.Location{ID: to, Name: "Attic"})
	refs := []domain.ItemRef{{ID: domain.NewItemID(), Name: "Stove"}}
	events := &fakeEventAppender{appendErr: errors.New("boom")}
	uow := &fakeBinUnitOfWork{store: store, items: &fakeBinItemIDLister{refs: refs}, events: events}
	mover := app.NewBinMover(uow, bins, locs, fixedClock(time.Now()), testLogger())
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Alice")

	_, err := mover.Move(context.Background(), actor, binID, to)
	if err == nil {
		t.Fatal("Move with a failing EventAppender = nil error, want one")
	}
	if len(events.events) != 0 {
		t.Error("a failing Append must record no event")
	}
	if store.bin.LocationID != from {
		t.Errorf("a failing Append must roll back the location swap too: LocationID = %v, want unchanged %v", store.bin.LocationID, from)
	}
}

func TestBinMover_Move_NoopRejected(t *testing.T) {
	loc := domain.NewLocationID()
	store, bins, locs, binID := movableFixture(loc)
	mover, events := newTestBinMover(store, bins, locs)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Alice")

	_, err := mover.Move(context.Background(), actor, binID, loc)
	if !errors.Is(err, domain.ErrBinAlreadyInLocation) {
		t.Errorf("Move(current location) = %v, want ErrBinAlreadyInLocation", err)
	}
	if store.bin.LocationID != loc {
		t.Error("rejected no-op move must not change the bin's location")
	}
	if store.moveCalls != 0 {
		t.Errorf("rejected no-op move must not call the repository Move, got %d calls", store.moveCalls)
	}
	if len(events.events) != 0 {
		t.Error("rejected no-op move must not append any event")
	}
}

func TestBinMover_Move_UnknownBinRejected(t *testing.T) {
	store, bins, locs, _ := movableFixture(domain.NewLocationID())
	bins.bin = nil
	bins.notFoundErr = domain.ErrBinNotFound
	mover, _ := newTestBinMover(store, bins, locs)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Alice")

	_, err := mover.Move(context.Background(), actor, domain.NewBinID(), domain.NewLocationID())
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("Move(unknown bin) = %v, want wrapped ErrBinNotFound", err)
	}
	if store.moveCalls != 0 {
		t.Errorf("Move(unknown bin) must not reach the repository, got %d calls", store.moveCalls)
	}
}

func TestBinMover_Move_UnknownLocationRejected(t *testing.T) {
	store, bins, locs, binID := movableFixture(domain.NewLocationID())
	locs.notFoundErr = domain.ErrLocationNotFound
	mover, _ := newTestBinMover(store, bins, locs)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Alice")

	_, err := mover.Move(context.Background(), actor, binID, domain.NewLocationID())
	if !errors.Is(err, domain.ErrLocationNotFound) {
		t.Errorf("Move(unknown location) = %v, want wrapped ErrLocationNotFound", err)
	}
	if store.moveCalls != 0 {
		t.Errorf("Move(unknown location) must not reach the repository, got %d calls", store.moveCalls)
	}
}

// TestBinMover_Move_SecondAttemptFailsAfterFirstSucceeds is the hermetic
// stand-in for a lost race, mirroring
// TestOperationService_RemoveFromBin_SecondAttemptFailsAfterFirstSucceeds:
// two sequential calls against the same fakeBinStore reproduce what a real
// concurrent second transaction sees after the first commits — the bin
// already relocated to the same target — without needing goroutines; the
// gated adapter suite proves the real concurrent case under an actual FOR
// UPDATE lock.
func TestBinMover_Move_SecondAttemptFailsAfterFirstSucceeds(t *testing.T) {
	from := domain.NewLocationID()
	to := domain.NewLocationID()
	store, bins, locs, binID := movableFixture(from)
	locs.add(domain.Location{ID: to, Name: "Attic"})
	mover, _ := newTestBinMover(store, bins, locs)
	first := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Alice")
	second := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Bob")

	if _, err := mover.Move(context.Background(), first, binID, to); err != nil {
		t.Fatalf("first Move: %v", err)
	}

	// bins fake reads FindVisibleByID off the same *domain.Bin store.bin
	// points to, so it already reflects the first move's committed state.
	_, err := mover.Move(context.Background(), second, binID, to)
	if !errors.Is(err, domain.ErrBinAlreadyInLocation) {
		t.Errorf("second Move (lost race) = %v, want ErrBinAlreadyInLocation", err)
	}
	if store.bin.LocationID != to {
		t.Errorf("the lost-race attempt must not change the winner's location: got %v, want %v", store.bin.LocationID, to)
	}
}
