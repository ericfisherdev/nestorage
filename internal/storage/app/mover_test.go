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
}

func (f *fakeBinStore) GetForUpdate(_ context.Context, id domain.BinID) (*domain.Bin, error) {
	if f.getForUpdateErr != nil {
		return nil, f.getForUpdateErr
	}
	if f.bin == nil || f.bin.ID != id {
		return nil, domain.ErrBinNotFound
	}
	cp := *f.bin
	return &cp, nil
}

func (f *fakeBinStore) Move(_ context.Context, id domain.BinID, target domain.LocationID, now time.Time) (int64, error) {
	f.moveCalls++
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

// fakeBinUnitOfWork runs fn directly against its single fakeBinStore with no
// real transactional isolation — the bin-move analog of fakeUnitOfWork.
type fakeBinUnitOfWork struct {
	store *fakeBinStore
}

func (u *fakeBinUnitOfWork) WithinBinTx(_ context.Context, fn func(app.BinStore) error) error {
	return fn(u.store)
}

// fakeLocationVisibility is an in-memory locationFinder fake: FindVisibleByID
// returns loc when its id matches, else notFoundErr — mirroring
// fakeBinVisibility's own shape for the location side of a move.
type fakeLocationVisibility struct {
	loc         *domain.Location
	notFoundErr error
}

func (f *fakeLocationVisibility) FindVisibleByID(_ context.Context, _ identity.Principal, id domain.LocationID) (*domain.Location, error) {
	if f.loc != nil && f.loc.ID == id {
		return f.loc, nil
	}
	return nil, f.notFoundErr
}

// movableFixture returns a fakeBinStore/fakeBinVisibility/fakeLocationVisibility
// trio around one bin sitting in from, for tests that move it to some other
// location.
func movableFixture(from domain.LocationID) (*fakeBinStore, *fakeBinVisibility, *fakeLocationVisibility, domain.BinID) {
	binID := domain.NewBinID()
	bin := &domain.Bin{ID: binID, LocationID: from}
	return &fakeBinStore{bin: bin}, &fakeBinVisibility{bin: bin}, &fakeLocationVisibility{}, binID
}

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func newTestBinMover(store *fakeBinStore, bins *fakeBinVisibility, locs *fakeLocationVisibility) *app.BinMover {
	return app.NewBinMover(&fakeBinUnitOfWork{store: store}, bins, locs, fixedClock(time.Now()), testLogger())
}

func TestNewBinMover_PanicsOnNilDeps(t *testing.T) {
	store, bins, locs, _ := movableFixture(domain.NewLocationID())
	uow := &fakeBinUnitOfWork{store: store}
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
	locs.loc = &domain.Location{ID: to}
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	mover := app.NewBinMover(&fakeBinUnitOfWork{store: store}, bins, locs, fixedClock(now), testLogger())

	holder := identity.NewUserID()
	actor := identity.NewUserPrincipal(holder, identity.RoleMember, "Alice")

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
}

func TestBinMover_Move_NoopRejected(t *testing.T) {
	loc := domain.NewLocationID()
	store, bins, locs, binID := movableFixture(loc)
	mover := newTestBinMover(store, bins, locs)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

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
}

func TestBinMover_Move_UnknownBinRejected(t *testing.T) {
	store, bins, locs, _ := movableFixture(domain.NewLocationID())
	bins.bin = nil
	bins.notFoundErr = domain.ErrBinNotFound
	mover := newTestBinMover(store, bins, locs)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

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
	mover := newTestBinMover(store, bins, locs)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

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
	locs.loc = &domain.Location{ID: to}
	mover := newTestBinMover(store, bins, locs)
	first := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")
	second := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Bob")

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
