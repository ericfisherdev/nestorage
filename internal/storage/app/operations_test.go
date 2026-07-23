package app_test

import (
	"context"
	"errors"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// fakeItemStore is an in-memory app.ItemStore fake standing in for the
// tx-bound ItemRepository a real transactor constructs. GetForUpdate always
// returns a copy of the stored item, so a caller's domain transition
// mutation (EnterBin/CheckOut/ReturnTo, which mutate the *domain.Item they
// are given) can never corrupt the fake's own state ahead of Move — the
// same "only Move actually persists" contract Postgres's transactional
// atomicity gives the real adapter.
type fakeItemStore struct {
	item            *domain.Item
	getForUpdateErr error
	moveErr         error
}

func (f *fakeItemStore) GetForUpdate(_ context.Context, id domain.ItemID) (*domain.Item, error) {
	if f.getForUpdateErr != nil {
		return nil, f.getForUpdateErr
	}
	if f.item == nil || f.item.ID != id {
		return nil, domain.ErrItemNotFound
	}
	cp := *f.item
	return &cp, nil
}

func (f *fakeItemStore) Move(_ context.Context, id domain.ItemID, dst domain.Placement) (int64, error) {
	if f.moveErr != nil {
		return 0, f.moveErr
	}
	if f.item == nil || f.item.ID != id {
		return 0, domain.ErrItemNotFound
	}
	f.item.CurrentBinID, f.item.HeldBy = dst.BinID, dst.HeldBy
	return 1, nil
}

// fakeUnitOfWork runs fn directly against its single fakeItemStore with no
// real transactional isolation. Calling the same OperationService method
// twice against one fakeUnitOfWork is what
// TestOperationService_RemoveFromBin_SecondAttemptFailsAfterFirstSucceeds
// uses to simulate, at the unit level, the lost race a real concurrent
// second transaction hits after the first commits: the fake's stored item
// has already flipped state by the time the second call's GetForUpdate
// reads it.
type fakeUnitOfWork struct {
	store *fakeItemStore
}

func (u *fakeUnitOfWork) WithinTx(_ context.Context, fn func(app.ItemStore) error) error {
	return fn(u.store)
}

// fakeBinVisibility is an in-memory binFinder fake: FindVisibleByID
// returns bin when its id matches, else notFoundErr.
type fakeBinVisibility struct {
	bin         *domain.Bin
	notFoundErr error
}

func (f *fakeBinVisibility) FindVisibleByID(_ context.Context, _ identity.Principal, id domain.BinID) (*domain.Bin, error) {
	if f.bin != nil && f.bin.ID == id {
		return f.bin, nil
	}
	return nil, f.notFoundErr
}

// binnedFixture returns a fakeItemStore/fakeBinVisibility pair around one
// item sitting in bin, for tests that need an in-bin starting state.
func binnedFixture() (*fakeItemStore, *fakeBinVisibility, domain.BinID) {
	binID := domain.NewBinID()
	bin := &domain.Bin{ID: binID}
	it := &domain.Item{ID: domain.NewItemID(), Name: "Stove", Quantity: 1, CurrentBinID: &binID}
	return &fakeItemStore{item: it}, &fakeBinVisibility{bin: bin}, binID
}

// heldFixture returns a fakeItemStore/fakeBinVisibility pair around one
// item checked out to holder, for tests that need a checked-out starting
// state.
func heldFixture(holder identity.UserID) (*fakeItemStore, *fakeBinVisibility) {
	it := &domain.Item{ID: domain.NewItemID(), Name: "Stove", Quantity: 1, HeldBy: &holder}
	return &fakeItemStore{item: it}, &fakeBinVisibility{notFoundErr: domain.ErrBinNotFound}
}

func newTestOperationService(store *fakeItemStore, bins *fakeBinVisibility) *app.OperationService {
	return app.NewOperationService(&fakeUnitOfWork{store: store}, bins, testLogger())
}

func TestNewOperationService_PanicsOnNilDeps(t *testing.T) {
	store, bins, _ := binnedFixture()
	uow := &fakeUnitOfWork{store: store}

	tests := []struct {
		name  string
		build func()
	}{
		{"nil transactor", func() { app.NewOperationService(nil, bins, testLogger()) }},
		{"nil binFinder", func() { app.NewOperationService(uow, nil, testLogger()) }},
		{"nil logger", func() { app.NewOperationService(uow, bins, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("NewOperationService did not panic")
				}
			}()
			tt.build()
		})
	}
}

func TestOperationService_AddToBin(t *testing.T) {
	holder := identity.NewUserID()
	store, bins := heldFixture(holder)
	destBinID := domain.NewBinID()
	bins.bin = &domain.Bin{ID: destBinID}
	svc := newTestOperationService(store, bins)
	actor := identity.NewUserPrincipal(holder, identity.RoleMember, "Alice")

	op, err := svc.AddToBin(context.Background(), actor, store.item.ID, destBinID)
	if err != nil {
		t.Fatalf("AddToBin: %v", err)
	}
	if op.Verb != app.OperationAdd {
		t.Errorf("AddToBin: Verb = %v, want %v", op.Verb, app.OperationAdd)
	}
	if op.Item.CurrentBinID == nil || *op.Item.CurrentBinID != destBinID {
		t.Errorf("AddToBin: CurrentBinID = %v, want %v", op.Item.CurrentBinID, destBinID)
	}
	if op.Item.HeldBy != nil {
		t.Error("AddToBin must clear HeldBy")
	}
	if op.BinID == nil || *op.BinID != destBinID {
		t.Errorf("AddToBin: Operation.BinID = %v, want %v", op.BinID, destBinID)
	}
	if op.Actor != actor.Actor() || op.UserID != actor.UserID {
		t.Errorf("AddToBin: Actor/UserID = %q/%v, want %q/%v", op.Actor, op.UserID, actor.Actor(), actor.UserID)
	}
}

func TestOperationService_AddToBin_AlreadyInBinRejected(t *testing.T) {
	store, bins, _ := binnedFixture()
	destBinID := domain.NewBinID()
	bins.bin = &domain.Bin{ID: destBinID}
	svc := newTestOperationService(store, bins)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")
	originalBin := *store.item.CurrentBinID

	_, err := svc.AddToBin(context.Background(), actor, store.item.ID, destBinID)
	if !errors.Is(err, domain.ErrItemAlreadyInBin) {
		t.Errorf("AddToBin(already in bin) = %v, want ErrItemAlreadyInBin", err)
	}
	if store.item.CurrentBinID == nil || *store.item.CurrentBinID != originalBin {
		t.Error("rejected AddToBin must not move the item (no partial write)")
	}
}

func TestOperationService_AddToBin_UnknownBinRejected(t *testing.T) {
	store, _ := heldFixture(identity.NewUserID())
	bins := &fakeBinVisibility{notFoundErr: domain.ErrBinNotFound}
	svc := newTestOperationService(store, bins)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

	_, err := svc.AddToBin(context.Background(), actor, store.item.ID, domain.NewBinID())
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("AddToBin(unknown bin) = %v, want wrapped ErrBinNotFound", err)
	}
}

func TestOperationService_AddToBin_UnknownItemRejected(t *testing.T) {
	destBinID := domain.NewBinID()
	store := &fakeItemStore{}
	bins := &fakeBinVisibility{bin: &domain.Bin{ID: destBinID}}
	svc := newTestOperationService(store, bins)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

	_, err := svc.AddToBin(context.Background(), actor, domain.NewItemID(), destBinID)
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("AddToBin(unknown item) = %v, want wrapped ErrItemNotFound", err)
	}
}

func TestOperationService_RemoveFromBin(t *testing.T) {
	store, bins, _ := binnedFixture()
	svc := newTestOperationService(store, bins)
	holder := identity.NewUserID()
	actor := identity.NewUserPrincipal(holder, identity.RoleMember, "Bob")

	op, err := svc.RemoveFromBin(context.Background(), actor, store.item.ID)
	if err != nil {
		t.Fatalf("RemoveFromBin: %v", err)
	}
	if op.Verb != app.OperationRemove {
		t.Errorf("RemoveFromBin: Verb = %v, want %v", op.Verb, app.OperationRemove)
	}
	if op.Item.HeldBy == nil || *op.Item.HeldBy != holder {
		t.Errorf("RemoveFromBin: HeldBy = %v, want %v", op.Item.HeldBy, holder)
	}
	if op.Item.CurrentBinID != nil {
		t.Error("RemoveFromBin must clear CurrentBinID")
	}
	if op.BinID != nil {
		t.Error("RemoveFromBin's Operation has no destination bin")
	}
	if op.Actor != actor.Actor() || op.UserID != actor.UserID {
		t.Errorf("RemoveFromBin: Actor/UserID = %q/%v, want %q/%v", op.Actor, op.UserID, actor.Actor(), actor.UserID)
	}
}

func TestOperationService_RemoveFromBin_IntegrationPrincipalRejected(t *testing.T) {
	store, bins, _ := binnedFixture()
	svc := newTestOperationService(store, bins)
	actor := identity.NewIntegrationPrincipal("Nestova")
	originalBin := *store.item.CurrentBinID

	_, err := svc.RemoveFromBin(context.Background(), actor, store.item.ID)
	if !errors.Is(err, domain.ErrHolderRequired) {
		t.Errorf("RemoveFromBin(integration principal) = %v, want ErrHolderRequired", err)
	}
	if store.item.CurrentBinID == nil || *store.item.CurrentBinID != originalBin {
		t.Error("rejected RemoveFromBin must not touch the item (no partial write)")
	}
}

func TestOperationService_RemoveFromBin_AlreadyCheckedOutRejected(t *testing.T) {
	existingHolder := identity.NewUserID()
	store, bins := heldFixture(existingHolder)
	svc := newTestOperationService(store, bins)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Bob")

	_, err := svc.RemoveFromBin(context.Background(), actor, store.item.ID)
	if !errors.Is(err, domain.ErrItemAlreadyCheckedOut) {
		t.Errorf("RemoveFromBin(already checked out) = %v, want ErrItemAlreadyCheckedOut", err)
	}
	if store.item.HeldBy == nil || *store.item.HeldBy != existingHolder {
		t.Error("rejected RemoveFromBin must not overwrite the existing holder (no partial write)")
	}
}

// TestOperationService_RemoveFromBin_SecondAttemptFailsAfterFirstSucceeds is
// the hermetic stand-in the ticket's own plan calls for: "a fake unitOfWork
// whose store flips state between GetForUpdate and SavePlacement simulates
// a lost race at the unit level." Two sequential calls against the same
// fakeItemStore reproduce exactly what a real concurrent second
// transaction sees after the first commits — the row already checked out —
// without needing goroutines; TestOperationService_RemoveFromBin's gated
// adapter-level sibling proves the real concurrent case.
func TestOperationService_RemoveFromBin_SecondAttemptFailsAfterFirstSucceeds(t *testing.T) {
	store, bins, _ := binnedFixture()
	svc := newTestOperationService(store, bins)
	first := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")
	second := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Bob")

	if _, err := svc.RemoveFromBin(context.Background(), first, store.item.ID); err != nil {
		t.Fatalf("first RemoveFromBin: %v", err)
	}

	_, err := svc.RemoveFromBin(context.Background(), second, store.item.ID)
	if !errors.Is(err, domain.ErrItemAlreadyCheckedOut) {
		t.Errorf("second RemoveFromBin (lost race) = %v, want ErrItemAlreadyCheckedOut", err)
	}
	if store.item.HeldBy == nil || *store.item.HeldBy != first.UserID {
		t.Error("the lost-race attempt must not overwrite the winner's hold")
	}
}

func TestOperationService_ReturnToBin(t *testing.T) {
	holder := identity.NewUserID()
	store, bins := heldFixture(holder)
	destBinID := domain.NewBinID()
	bins.bin = &domain.Bin{ID: destBinID}
	svc := newTestOperationService(store, bins)
	actor := identity.NewUserPrincipal(holder, identity.RoleMember, "Alice")

	op, err := svc.ReturnToBin(context.Background(), actor, store.item.ID, destBinID)
	if err != nil {
		t.Fatalf("ReturnToBin: %v", err)
	}
	if op.Verb != app.OperationReturn {
		t.Errorf("ReturnToBin: Verb = %v, want %v", op.Verb, app.OperationReturn)
	}
	if op.Item.CurrentBinID == nil || *op.Item.CurrentBinID != destBinID {
		t.Errorf("ReturnToBin: CurrentBinID = %v, want %v", op.Item.CurrentBinID, destBinID)
	}
	if op.Item.HeldBy != nil {
		t.Error("ReturnToBin must clear HeldBy")
	}
	if op.BinID == nil || *op.BinID != destBinID {
		t.Errorf("ReturnToBin: Operation.BinID = %v, want %v", op.BinID, destBinID)
	}
}

func TestOperationService_ReturnToBin_NotCheckedOutRejected(t *testing.T) {
	store, bins, originalBinID := binnedFixture()
	destBinID := domain.NewBinID()
	bins.bin = &domain.Bin{ID: destBinID}
	svc := newTestOperationService(store, bins)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

	_, err := svc.ReturnToBin(context.Background(), actor, store.item.ID, destBinID)
	if !errors.Is(err, domain.ErrItemNotCheckedOut) {
		t.Errorf("ReturnToBin(not checked out) = %v, want ErrItemNotCheckedOut", err)
	}
	if store.item.CurrentBinID == nil || *store.item.CurrentBinID != originalBinID {
		t.Error("rejected ReturnToBin must not move the item (no partial write)")
	}
}

func TestOperationService_ReturnToBin_UnknownBinRejected(t *testing.T) {
	store, bins := heldFixture(identity.NewUserID())
	bins.notFoundErr = domain.ErrBinNotFound
	svc := newTestOperationService(store, bins)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

	_, err := svc.ReturnToBin(context.Background(), actor, store.item.ID, domain.NewBinID())
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("ReturnToBin(unknown bin) = %v, want wrapped ErrBinNotFound", err)
	}
}
