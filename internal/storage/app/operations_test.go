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

// fakeUnitOfWork runs fn directly against its single fakeItemStore/
// fakeEventAppender pair, simulating just enough transactional behavior for
// a hermetic unit test: on a fn error, it restores the fakeItemStore's item
// to what it was before fn ran (a real pgx transaction's rollback would undo
// the same in-progress Move), so TestOperationService_RemoveFromBin_
// EventAppendFailureAbortsOperation can assert "the fake store observed a
// rollback" without a real database. Calling the same OperationService
// method twice against one fakeUnitOfWork is what
// TestOperationService_RemoveFromBin_SecondAttemptFailsAfterFirstSucceeds
// uses to simulate, at the unit level, the lost race a real concurrent
// second transaction hits after the first commits: the fake's stored item
// has already flipped state by the time the second call's GetForUpdate
// reads it.
type fakeUnitOfWork struct {
	store  *fakeItemStore
	events *fakeEventAppender
}

func (u *fakeUnitOfWork) WithinTx(_ context.Context, fn func(app.OperationStores) error) error {
	var before *domain.Item
	if u.store.item != nil {
		cp := *u.store.item
		before = &cp
	}
	err := fn(app.OperationStores{Items: u.store, Events: u.events})
	if err != nil && before != nil {
		*u.store.item = *before
	}
	return err
}

// fakeBinVisibility is an in-memory binFinder fake: FindVisibleByID returns
// a copy of bin when its id matches, else notFoundErr. A copy, not bin
// itself, mirrors fakeItemStore.GetForUpdate's own copy-on-read rationale:
// two independent reads of the same row (a real FindVisibleByID SELECT and a
// later, separate GetForUpdate SELECT ... FOR UPDATE) are always distinct Go
// values in production, so a caller mutating one (BinMover.Move's own
// bin.MoveTo(target) no-op check, mover_test.go) must never be able to
// corrupt what a later read of the same fake sees.
type fakeBinVisibility struct {
	bin         *domain.Bin
	notFoundErr error
}

func (f *fakeBinVisibility) FindVisibleByID(_ context.Context, _ identity.Principal, id domain.BinID) (*domain.Bin, error) {
	if f.bin != nil && f.bin.ID == id {
		cp := *f.bin
		return &cp, nil
	}
	return nil, f.notFoundErr
}

// binnedFixture returns a fakeItemStore/fakeBinVisibility pair around one
// item sitting in bin, for tests that need an in-bin starting state.
func binnedFixture() (*fakeItemStore, *fakeBinVisibility, domain.BinID) {
	binID := domain.NewBinID()
	bin := &domain.Bin{ID: binID, Code: "A1", Name: "Bin A"}
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

// newTestOperationService wires an OperationService over store/bins and a
// fresh fakeEventAppender, returning the appender alongside the service so
// a test that cares which events were appended can inspect it directly.
func newTestOperationService(store *fakeItemStore, bins *fakeBinVisibility) (*app.OperationService, *fakeEventAppender) {
	events := &fakeEventAppender{}
	uow := &fakeUnitOfWork{store: store, events: events}
	return app.NewOperationService(uow, bins, testLogger()), events
}

func TestNewOperationService_PanicsOnNilDeps(t *testing.T) {
	store, bins, _ := binnedFixture()
	uow := &fakeUnitOfWork{store: store, events: &fakeEventAppender{}}

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
	bins.bin = &domain.Bin{ID: destBinID, Code: "B2", Name: "Bin B"}
	svc, _ := newTestOperationService(store, bins)
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

// TestOperationService_AddToBin_EmitsExactlyOneAddedEvent proves NSTR-41's
// per-operation event contract for AddToBin: exactly one EventAdded row,
// naming the destination bin, no note (AddToBin carries none — see R16 of
// NSTR-41's own reconciliation).
func TestOperationService_AddToBin_EmitsExactlyOneAddedEvent(t *testing.T) {
	holder := identity.NewUserID()
	store, bins := heldFixture(holder)
	destBinID := domain.NewBinID()
	bins.bin = &domain.Bin{ID: destBinID, Code: "B2", Name: "Bin B"}
	svc, events := newTestOperationService(store, bins)
	actor := identity.NewUserPrincipal(holder, identity.RoleMember, "Alice")

	if _, err := svc.AddToBin(context.Background(), actor, store.item.ID, destBinID); err != nil {
		t.Fatalf("AddToBin: %v", err)
	}

	if len(events.events) != 1 {
		t.Fatalf("AddToBin appended %d events, want exactly 1", len(events.events))
	}
	got := events.events[0]
	if got.Kind != domain.EventAdded {
		t.Errorf("event.Kind = %v, want %v", got.Kind, domain.EventAdded)
	}
	if got.BinID == nil || *got.BinID != destBinID {
		t.Errorf("event.BinID = %v, want %v", got.BinID, destBinID)
	}
	if got.Note != "" {
		t.Errorf("event.Note = %q, want empty for AddToBin", got.Note)
	}
}

func TestOperationService_AddToBin_AlreadyInBinRejected(t *testing.T) {
	store, bins, _ := binnedFixture()
	destBinID := domain.NewBinID()
	bins.bin = &domain.Bin{ID: destBinID}
	svc, events := newTestOperationService(store, bins)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")
	originalBin := *store.item.CurrentBinID

	_, err := svc.AddToBin(context.Background(), actor, store.item.ID, destBinID)
	if !errors.Is(err, domain.ErrItemAlreadyInBin) {
		t.Errorf("AddToBin(already in bin) = %v, want ErrItemAlreadyInBin", err)
	}
	if store.item.CurrentBinID == nil || *store.item.CurrentBinID != originalBin {
		t.Error("rejected AddToBin must not move the item (no partial write)")
	}
	if len(events.events) != 0 {
		t.Error("rejected AddToBin must not append an event")
	}
}

func TestOperationService_AddToBin_UnknownBinRejected(t *testing.T) {
	store, _ := heldFixture(identity.NewUserID())
	bins := &fakeBinVisibility{notFoundErr: domain.ErrBinNotFound}
	svc, _ := newTestOperationService(store, bins)
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
	svc, _ := newTestOperationService(store, bins)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

	_, err := svc.AddToBin(context.Background(), actor, domain.NewItemID(), destBinID)
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("AddToBin(unknown item) = %v, want wrapped ErrItemNotFound", err)
	}
}

func TestOperationService_RemoveFromBin(t *testing.T) {
	store, bins, _ := binnedFixture()
	svc, _ := newTestOperationService(store, bins)
	holder := identity.NewUserID()
	actor := identity.NewUserPrincipal(holder, identity.RoleMember, "Bob")

	op, err := svc.RemoveFromBin(context.Background(), actor, store.item.ID, nil)
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

// TestOperationService_RemoveFromBin_EmitsExactlyOneRemovedEvent proves
// NSTR-41's reconciliation R5: the removed event's BinID/BinLabel snapshot
// the SOURCE bin (the one the item was in before CheckOut cleared it in
// memory), not nil, and carry the supplied note.
func TestOperationService_RemoveFromBin_EmitsExactlyOneRemovedEvent(t *testing.T) {
	store, bins, sourceBinID := binnedFixture()
	svc, events := newTestOperationService(store, bins)
	holder := identity.NewUserID()
	actor := identity.NewUserPrincipal(holder, identity.RoleMember, "Bob")
	note := "smells like gasoline"

	if _, err := svc.RemoveFromBin(context.Background(), actor, store.item.ID, &note); err != nil {
		t.Fatalf("RemoveFromBin: %v", err)
	}

	if len(events.events) != 1 {
		t.Fatalf("RemoveFromBin appended %d events, want exactly 1", len(events.events))
	}
	got := events.events[0]
	if got.Kind != domain.EventRemoved {
		t.Errorf("event.Kind = %v, want %v", got.Kind, domain.EventRemoved)
	}
	if got.BinID == nil || *got.BinID != sourceBinID {
		t.Errorf("event.BinID = %v, want the source bin %v (R5: snapshot before the guard clears it)", got.BinID, sourceBinID)
	}
	if got.Note != note {
		t.Errorf("event.Note = %q, want %q", got.Note, note)
	}
}

func TestOperationService_RemoveFromBin_IntegrationPrincipalRejected(t *testing.T) {
	store, bins, _ := binnedFixture()
	svc, events := newTestOperationService(store, bins)
	actor := identity.NewIntegrationPrincipal("Nestova")
	originalBin := *store.item.CurrentBinID

	_, err := svc.RemoveFromBin(context.Background(), actor, store.item.ID, nil)
	if !errors.Is(err, domain.ErrHolderRequired) {
		t.Errorf("RemoveFromBin(integration principal) = %v, want ErrHolderRequired", err)
	}
	if store.item.CurrentBinID == nil || *store.item.CurrentBinID != originalBin {
		t.Error("rejected RemoveFromBin must not touch the item (no partial write)")
	}
	if len(events.events) != 0 {
		t.Error("rejected RemoveFromBin must not append an event")
	}
}

func TestOperationService_RemoveFromBin_AlreadyCheckedOutRejected(t *testing.T) {
	existingHolder := identity.NewUserID()
	store, bins := heldFixture(existingHolder)
	svc, events := newTestOperationService(store, bins)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Bob")

	_, err := svc.RemoveFromBin(context.Background(), actor, store.item.ID, nil)
	if !errors.Is(err, domain.ErrItemAlreadyCheckedOut) {
		t.Errorf("RemoveFromBin(already checked out) = %v, want ErrItemAlreadyCheckedOut", err)
	}
	if store.item.HeldBy == nil || *store.item.HeldBy != existingHolder {
		t.Error("rejected RemoveFromBin must not overwrite the existing holder (no partial write)")
	}
	if len(events.events) != 0 {
		t.Error("rejected RemoveFromBin must not append an event")
	}
}

// TestOperationService_RemoveFromBin_EventAppendFailureAbortsOperation is
// the ticket's own required proof: a failing EventAppender makes the whole
// operation return an error and leaves the fake store observing a rollback
// — the placement swap Move already performed is undone, exactly as a real
// pgx transaction's rollback would undo it, so the state change and its
// event either both land or neither does.
func TestOperationService_RemoveFromBin_EventAppendFailureAbortsOperation(t *testing.T) {
	store, bins, sourceBinID := binnedFixture()
	events := &fakeEventAppender{appendErr: errors.New("boom")}
	uow := &fakeUnitOfWork{store: store, events: events}
	svc := app.NewOperationService(uow, bins, testLogger())
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Bob")

	_, err := svc.RemoveFromBin(context.Background(), actor, store.item.ID, nil)
	if err == nil {
		t.Fatal("RemoveFromBin with a failing EventAppender = nil error, want one")
	}
	if len(events.events) != 0 {
		t.Error("a failing Append must record no event")
	}
	if store.item.CurrentBinID == nil || *store.item.CurrentBinID != sourceBinID {
		t.Errorf("a failing Append must roll back the placement swap too: CurrentBinID = %v, want unchanged %v", store.item.CurrentBinID, sourceBinID)
	}
	if store.item.HeldBy != nil {
		t.Error("a failing Append must roll back HeldBy too: item must not remain checked out")
	}
}

// TestOperationService_EventAttribution is table-driven across the three
// credential kinds NSTR-41's own reconciliation lists. Principal carries no
// field distinguishing a session credential from a device-token one — both
// resolve to identity.KindUser (see identity.Principal's own doc) — so the
// "device-token principal" row exercises literally the same code path as
// "session principal"; both are kept to match the ticket's three-row table
// rather than collapsing it to two. AddToBin is used as the exercised
// operation since, unlike RemoveFromBin, it has no ErrHolderRequired
// precondition and so accepts every principal kind.
func TestOperationService_EventAttribution(t *testing.T) {
	tests := []struct {
		name  string
		actor identity.Principal
	}{
		{"session principal", identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")},
		{"device-token principal", identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Bob")},
		{"integration principal", identity.NewIntegrationPrincipal("Nestova")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			holder := identity.NewUserID()
			store, bins := heldFixture(holder)
			destBinID := domain.NewBinID()
			bins.bin = &domain.Bin{ID: destBinID, Code: "B2", Name: "Bin B"}
			svc, events := newTestOperationService(store, bins)

			if _, err := svc.AddToBin(context.Background(), tt.actor, store.item.ID, destBinID); err != nil {
				t.Fatalf("AddToBin: %v", err)
			}
			if len(events.events) != 1 {
				t.Fatalf("AddToBin appended %d events, want exactly 1", len(events.events))
			}
			got := events.events[0]
			if got.ActorKind != tt.actor.Kind {
				t.Errorf("ActorKind = %v, want %v", got.ActorKind, tt.actor.Kind)
			}
			if got.ActorUserID != tt.actor.UserID {
				t.Errorf("ActorUserID = %v, want %v", got.ActorUserID, tt.actor.UserID)
			}
			if got.ActorLabel != tt.actor.Actor() {
				t.Errorf("ActorLabel = %q, want %q", got.ActorLabel, tt.actor.Actor())
			}
		})
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
	svc, _ := newTestOperationService(store, bins)
	first := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")
	second := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Bob")

	if _, err := svc.RemoveFromBin(context.Background(), first, store.item.ID, nil); err != nil {
		t.Fatalf("first RemoveFromBin: %v", err)
	}

	_, err := svc.RemoveFromBin(context.Background(), second, store.item.ID, nil)
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
	svc, _ := newTestOperationService(store, bins)
	actor := identity.NewUserPrincipal(holder, identity.RoleMember, "Alice")

	op, err := svc.ReturnToBin(context.Background(), actor, store.item.ID, destBinID, nil)
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

// TestOperationService_ReturnToBin_EmitsExactlyOneReturnedEvent proves
// NSTR-41's per-operation event contract for ReturnToBin: exactly one
// EventReturned row, naming the destination bin and carrying note.
func TestOperationService_ReturnToBin_EmitsExactlyOneReturnedEvent(t *testing.T) {
	holder := identity.NewUserID()
	store, bins := heldFixture(holder)
	destBinID := domain.NewBinID()
	bins.bin = &domain.Bin{ID: destBinID, Code: "B2", Name: "Bin B"}
	svc, events := newTestOperationService(store, bins)
	actor := identity.NewUserPrincipal(holder, identity.RoleMember, "Alice")
	note := "back on the shelf"

	if _, err := svc.ReturnToBin(context.Background(), actor, store.item.ID, destBinID, &note); err != nil {
		t.Fatalf("ReturnToBin: %v", err)
	}

	if len(events.events) != 1 {
		t.Fatalf("ReturnToBin appended %d events, want exactly 1", len(events.events))
	}
	got := events.events[0]
	if got.Kind != domain.EventReturned {
		t.Errorf("event.Kind = %v, want %v", got.Kind, domain.EventReturned)
	}
	if got.BinID == nil || *got.BinID != destBinID {
		t.Errorf("event.BinID = %v, want %v", got.BinID, destBinID)
	}
	if got.Note != note {
		t.Errorf("event.Note = %q, want %q", got.Note, note)
	}
}

func TestOperationService_ReturnToBin_NotCheckedOutRejected(t *testing.T) {
	store, bins, originalBinID := binnedFixture()
	destBinID := domain.NewBinID()
	bins.bin = &domain.Bin{ID: destBinID}
	svc, events := newTestOperationService(store, bins)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

	_, err := svc.ReturnToBin(context.Background(), actor, store.item.ID, destBinID, nil)
	if !errors.Is(err, domain.ErrItemNotCheckedOut) {
		t.Errorf("ReturnToBin(not checked out) = %v, want ErrItemNotCheckedOut", err)
	}
	if store.item.CurrentBinID == nil || *store.item.CurrentBinID != originalBinID {
		t.Error("rejected ReturnToBin must not move the item (no partial write)")
	}
	if len(events.events) != 0 {
		t.Error("rejected ReturnToBin must not append an event")
	}
}

func TestOperationService_ReturnToBin_UnknownBinRejected(t *testing.T) {
	store, bins := heldFixture(identity.NewUserID())
	bins.notFoundErr = domain.ErrBinNotFound
	svc, _ := newTestOperationService(store, bins)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

	_, err := svc.ReturnToBin(context.Background(), actor, store.item.ID, domain.NewBinID(), nil)
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("ReturnToBin(unknown bin) = %v, want wrapped ErrBinNotFound", err)
	}
}
