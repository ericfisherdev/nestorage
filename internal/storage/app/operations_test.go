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

	// lastGetForUpdateHouseholdID/lastMoveHouseholdID record the householdID
	// OperationService.transition actually passed through — NSTR-131 threads
	// householdID into both calls (see transition's own doc); recording it
	// here, rather than silently accepting-and-ignoring it, is what lets
	// TestOperationService_AddToBin_Success and friends catch a regression
	// that dropped or zeroed that argument before it ever reaches a real
	// ItemRepository.
	lastGetForUpdateHouseholdID identity.HouseholdID
	lastMoveHouseholdID         identity.HouseholdID
}

func (f *fakeItemStore) GetForUpdate(_ context.Context, householdID identity.HouseholdID, id domain.ItemID) (*domain.Item, error) {
	f.lastGetForUpdateHouseholdID = householdID
	if f.getForUpdateErr != nil {
		return nil, f.getForUpdateErr
	}
	if f.item == nil || f.item.ID != id {
		return nil, domain.ErrItemNotFound
	}
	cp := *f.item
	return &cp, nil
}

func (f *fakeItemStore) Move(_ context.Context, householdID identity.HouseholdID, id domain.ItemID, dst domain.Placement) (int64, error) {
	f.lastMoveHouseholdID = householdID
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
	store    *fakeItemStore
	events   *fakeEventAppender
	requests *fakeReturnRequestFulfiller
}

func (u *fakeUnitOfWork) WithinTx(_ context.Context, fn func(app.OperationStores) error) error {
	var before *domain.Item
	if u.store.item != nil {
		cp := *u.store.item
		before = &cp
	}
	// beforeEvents snapshots the appender's own slice too: NSTR-43's own
	// fulfilment can fail AFTER a successful event Append (Append records
	// unconditionally into u.events.events, unlike a real pgx statement
	// that only lands on COMMIT), so restoring only the item on error would
	// leave a "committed" event behind a rolled-back transaction — the same
	// no-partial-write guarantee TestOperationService_
	// RemoveFromBin_EventAppendFailureAbortsOperation already proves for
	// the append-fails-first case, extended to cover a later failure too.
	beforeEvents := append([]domain.ItemEvent(nil), u.events.events...)
	requests := u.requests
	if requests == nil {
		requests = &fakeReturnRequestFulfiller{}
	}
	err := fn(app.OperationStores{Items: u.store, Events: u.events, ReturnRequests: requests})
	if err != nil {
		if before != nil {
			*u.store.item = *before
		}
		u.events.events = beforeEvents
	}
	return err
}

// fakeReturnRequestFulfiller is an in-memory app.ReturnRequestFulfiller
// fake standing in for the tx-bound ReturnRequestRepository a real
// transactor constructs: FulfillOpenForItem flips every open request on
// itemID to fulfilled (via domain.ReturnRequest.Fulfill itself, so the
// fake exercises the same guard the real adapter's SQL enforces) and
// returns the flipped copies, or fails with fulfillErr (flipping nothing)
// when set.
type fakeReturnRequestFulfiller struct {
	requests   []domain.ReturnRequest
	fulfillErr error

	// lastHouseholdID records the householdID transition actually passed —
	// see fakeItemStore's identical rationale above.
	lastHouseholdID identity.HouseholdID
}

func (f *fakeReturnRequestFulfiller) FulfillOpenForItem(_ context.Context, householdID identity.HouseholdID, itemID domain.ItemID, at time.Time) ([]domain.ReturnRequest, error) {
	f.lastHouseholdID = householdID
	if f.fulfillErr != nil {
		return nil, f.fulfillErr
	}
	var flipped []domain.ReturnRequest
	for i := range f.requests {
		r := &f.requests[i]
		if r.ItemID != itemID || r.Status != domain.ReturnRequestStatusOpen {
			continue
		}
		if err := r.Fulfill(at); err != nil {
			return nil, err
		}
		flipped = append(flipped, *r)
	}
	return flipped, nil
}

// fakeUserLabelResolver is an in-memory app.userLabelResolver fake:
// FindByID returns users[id] when present, else identity.ErrUserNotFound —
// standing in for identity.UserRepository, which
// OperationService.buildFulfilledNotification resolves a fulfilled
// request's requester display name through.
type fakeUserLabelResolver struct {
	users map[identity.UserID]*identity.User
}

func (f *fakeUserLabelResolver) FindByID(_ context.Context, id identity.UserID) (*identity.User, error) {
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return nil, identity.ErrUserNotFound
}

// fakeReturnRequestNotifier is an in-memory app.ReturnRequestNotifier fake
// recording every call it receives, so a fulfilment test can assert
// ReturnRequestsFulfilled was (or was not) called, and with what.
type fakeReturnRequestNotifier struct {
	requested []app.ReturnRequestNotification
	fulfilled [][]app.ReturnRequestNotification
}

func (f *fakeReturnRequestNotifier) ReturnRequested(_ context.Context, n app.ReturnRequestNotification) {
	f.requested = append(f.requested, n)
}

func (f *fakeReturnRequestNotifier) ReturnRequestsFulfilled(_ context.Context, ns []app.ReturnRequestNotification) {
	f.fulfilled = append(f.fulfilled, ns)
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
// a test that cares which events were appended can inspect it directly. Its
// return-request collaborators are harmless no-ops (an empty
// fakeReturnRequestFulfiller, an empty fakeUserLabelResolver, a
// fakeReturnRequestNotifier no test here inspects) — every test using this
// constructor predates NSTR-43 and exercises none of that path directly;
// see newTestOperationServiceWithReturnRequests for the tests that do.
func newTestOperationService(store *fakeItemStore, bins *fakeBinVisibility) (*app.OperationService, *fakeEventAppender) {
	events := &fakeEventAppender{}
	uow := &fakeUnitOfWork{store: store, events: events, requests: &fakeReturnRequestFulfiller{}}
	users := &fakeUserLabelResolver{}
	notifier := &fakeReturnRequestNotifier{}
	return app.NewOperationService(uow, bins, users, notifier, testLogger()), events
}

// newTestOperationServiceWithReturnRequests wires an OperationService like
// newTestOperationService, but over a fakeReturnRequestFulfiller seeded with
// openRequests and users (requester id -> display name, for
// buildFulfilledNotification's own lookup), returning the fulfiller and
// notifier alongside the service so NSTR-43's own fulfilment tests can
// inspect both — no other test in this file needs either, so this is kept
// separate rather than growing newTestOperationService's own return list.
func newTestOperationServiceWithReturnRequests(store *fakeItemStore, bins *fakeBinVisibility, openRequests []domain.ReturnRequest, users map[identity.UserID]*identity.User) (*app.OperationService, *fakeEventAppender, *fakeReturnRequestFulfiller, *fakeReturnRequestNotifier) {
	events := &fakeEventAppender{}
	requests := &fakeReturnRequestFulfiller{requests: openRequests}
	uow := &fakeUnitOfWork{store: store, events: events, requests: requests}
	notifier := &fakeReturnRequestNotifier{}
	svc := app.NewOperationService(uow, bins, &fakeUserLabelResolver{users: users}, notifier, testLogger())
	return svc, events, requests, notifier
}

func TestNewOperationService_PanicsOnNilDeps(t *testing.T) {
	store, bins, _ := binnedFixture()
	uow := &fakeUnitOfWork{store: store, events: &fakeEventAppender{}}
	users := &fakeUserLabelResolver{}
	notifier := &fakeReturnRequestNotifier{}

	tests := []struct {
		name  string
		build func()
	}{
		{"nil transactor", func() { app.NewOperationService(nil, bins, users, notifier, testLogger()) }},
		{"nil binFinder", func() { app.NewOperationService(uow, nil, users, notifier, testLogger()) }},
		{"nil userLabelResolver", func() { app.NewOperationService(uow, bins, nil, notifier, testLogger()) }},
		{"nil ReturnRequestNotifier", func() { app.NewOperationService(uow, bins, users, nil, testLogger()) }},
		{"nil logger", func() { app.NewOperationService(uow, bins, users, notifier, nil) }},
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
	actor := identity.NewUserPrincipal(holder, identity.RoleAdult, "Alice")
	actor.HouseholdID = identity.NewHouseholdID()

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
	// NSTR-131: transition must pass actor's own household through to both
	// the tx-bound GetForUpdate and Move calls.
	if store.lastGetForUpdateHouseholdID != actor.HouseholdID {
		t.Errorf("GetForUpdate's householdID = %v, want actor's own %v", store.lastGetForUpdateHouseholdID, actor.HouseholdID)
	}
	if store.lastMoveHouseholdID != actor.HouseholdID {
		t.Errorf("Move's householdID = %v, want actor's own %v", store.lastMoveHouseholdID, actor.HouseholdID)
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
	actor := identity.NewUserPrincipal(holder, identity.RoleAdult, "Alice")

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
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Alice")
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
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Alice")

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
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Alice")

	_, err := svc.AddToBin(context.Background(), actor, domain.NewItemID(), destBinID)
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("AddToBin(unknown item) = %v, want wrapped ErrItemNotFound", err)
	}
}

func TestOperationService_RemoveFromBin(t *testing.T) {
	store, bins, _ := binnedFixture()
	svc, _ := newTestOperationService(store, bins)
	holder := identity.NewUserID()
	actor := identity.NewUserPrincipal(holder, identity.RoleAdult, "Bob")

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
	actor := identity.NewUserPrincipal(holder, identity.RoleAdult, "Bob")
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
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Bob")

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
	svc := app.NewOperationService(uow, bins, &fakeUserLabelResolver{}, &fakeReturnRequestNotifier{}, testLogger())
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Bob")

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
		{"session principal", identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Alice")},
		{"device-token principal", identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Bob")},
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
	first := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Alice")
	second := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Bob")

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
	actor := identity.NewUserPrincipal(holder, identity.RoleAdult, "Alice")

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
	actor := identity.NewUserPrincipal(holder, identity.RoleAdult, "Alice")
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
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Alice")

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
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Alice")

	_, err := svc.ReturnToBin(context.Background(), actor, store.item.ID, domain.NewBinID(), nil)
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("ReturnToBin(unknown bin) = %v, want wrapped ErrBinNotFound", err)
	}
}

// openReturnRequestOn returns an open domain.ReturnRequest for itemID
// raised by requester against holder — the fixture NSTR-43's own
// fulfilment tests seed fakeReturnRequestFulfiller with.
func openReturnRequestOn(itemID domain.ItemID, requester, holder identity.UserID) domain.ReturnRequest {
	return domain.ReturnRequest{
		ID: domain.NewReturnRequestID(), ItemID: itemID,
		RequesterID: requester, HolderID: holder, Status: domain.ReturnRequestStatusOpen,
	}
}

// TestOperationService_AddToBin_FulfilsOpenReturnRequests proves NSTR-43's
// own held-to-bin rule for AddToBin (Item.EnterBin succeeds on a held item,
// so AddToBin is a fulfilling transition exactly like ReturnToBin): every
// open request on the item is flipped to fulfilled inside the same
// transaction, surfaced on Operation.FulfilledReturnRequests, and the
// notifier's ReturnRequestsFulfilled is called exactly once with a
// matching notification carrying the resolved requester's own display
// name.
func TestOperationService_AddToBin_FulfilsOpenReturnRequests(t *testing.T) {
	holder := identity.NewUserID()
	store, bins := heldFixture(holder)
	destBinID := domain.NewBinID()
	bins.bin = &domain.Bin{ID: destBinID, Code: "B2", Name: "Bin B"}
	requester := identity.NewUserID()
	open := openReturnRequestOn(store.item.ID, requester, holder)
	users := map[identity.UserID]*identity.User{requester: {ID: requester, DisplayName: "Riley"}}
	svc, _, requests, notifier := newTestOperationServiceWithReturnRequests(store, bins, []domain.ReturnRequest{open}, users)
	actor := identity.NewUserPrincipal(holder, identity.RoleAdult, "Alice")
	actor.HouseholdID = identity.NewHouseholdID()

	op, err := svc.AddToBin(context.Background(), actor, store.item.ID, destBinID)
	if err != nil {
		t.Fatalf("AddToBin: %v", err)
	}

	if len(op.FulfilledReturnRequests) != 1 {
		t.Fatalf("Operation.FulfilledReturnRequests = %d requests, want exactly 1", len(op.FulfilledReturnRequests))
	}
	// NSTR-131: transition must pass actor's own household through to
	// FulfillOpenForItem too, not only GetForUpdate/Move.
	if requests.lastHouseholdID != actor.HouseholdID {
		t.Errorf("FulfillOpenForItem's householdID = %v, want actor's own %v", requests.lastHouseholdID, actor.HouseholdID)
	}
	got := op.FulfilledReturnRequests[0]
	if got.ID != open.ID || got.Status != domain.ReturnRequestStatusFulfilled || got.ResolvedAt == nil {
		t.Errorf("fulfilled request = %+v, want id %v, status fulfilled, ResolvedAt set", got, open.ID)
	}

	if len(notifier.fulfilled) != 1 {
		t.Fatalf("notifier.ReturnRequestsFulfilled called %d times, want exactly 1", len(notifier.fulfilled))
	}
	batch := notifier.fulfilled[0]
	if len(batch) != 1 {
		t.Fatalf("notified batch has %d notifications, want exactly 1", len(batch))
	}
	if batch[0].RequestID != open.ID || batch[0].RequesterID != requester || batch[0].RequesterLabel != "Riley" {
		t.Errorf("notification = %+v, want RequestID %v, RequesterID %v, RequesterLabel %q", batch[0], open.ID, requester, "Riley")
	}
	if batch[0].ItemID != store.item.ID || batch[0].ItemName != store.item.Name {
		t.Errorf("notification item = %v/%q, want %v/%q", batch[0].ItemID, batch[0].ItemName, store.item.ID, store.item.Name)
	}
}

// TestOperationService_ReturnToBin_FulfilsOpenReturnRequests is
// AddToBin_FulfilsOpenReturnRequests' ReturnToBin sibling — the ticket's own
// primary example of a fulfilling transition.
func TestOperationService_ReturnToBin_FulfilsOpenReturnRequests(t *testing.T) {
	holder := identity.NewUserID()
	store, bins := heldFixture(holder)
	destBinID := domain.NewBinID()
	bins.bin = &domain.Bin{ID: destBinID}
	requester := identity.NewUserID()
	open := openReturnRequestOn(store.item.ID, requester, holder)
	users := map[identity.UserID]*identity.User{requester: {ID: requester, DisplayName: "Riley"}}
	svc, _, _, notifier := newTestOperationServiceWithReturnRequests(store, bins, []domain.ReturnRequest{open}, users)
	actor := identity.NewUserPrincipal(holder, identity.RoleAdult, "Alice")

	op, err := svc.ReturnToBin(context.Background(), actor, store.item.ID, destBinID, nil)
	if err != nil {
		t.Fatalf("ReturnToBin: %v", err)
	}

	if len(op.FulfilledReturnRequests) != 1 || op.FulfilledReturnRequests[0].ID != open.ID {
		t.Fatalf("Operation.FulfilledReturnRequests = %+v, want exactly [%v]", op.FulfilledReturnRequests, open.ID)
	}
	if len(notifier.fulfilled) != 1 || len(notifier.fulfilled[0]) != 1 {
		t.Fatalf("notifier.ReturnRequestsFulfilled = %v, want exactly one call with one notification", notifier.fulfilled)
	}
}

// TestOperationService_ReturnToBin_MultipleOpenRequestsAllFulfilled proves
// FulfillOpenForItem-driven fulfilment fans out to every open requester on
// the item, not only the first.
func TestOperationService_ReturnToBin_MultipleOpenRequestsAllFulfilled(t *testing.T) {
	holder := identity.NewUserID()
	store, bins := heldFixture(holder)
	destBinID := domain.NewBinID()
	bins.bin = &domain.Bin{ID: destBinID}
	first, second := identity.NewUserID(), identity.NewUserID()
	open := []domain.ReturnRequest{
		openReturnRequestOn(store.item.ID, first, holder),
		openReturnRequestOn(store.item.ID, second, holder),
	}
	svc, _, _, notifier := newTestOperationServiceWithReturnRequests(store, bins, open, nil)
	actor := identity.NewUserPrincipal(holder, identity.RoleAdult, "Alice")

	op, err := svc.ReturnToBin(context.Background(), actor, store.item.ID, destBinID, nil)
	if err != nil {
		t.Fatalf("ReturnToBin: %v", err)
	}
	if len(op.FulfilledReturnRequests) != 2 {
		t.Fatalf("Operation.FulfilledReturnRequests = %d requests, want exactly 2", len(op.FulfilledReturnRequests))
	}
	if len(notifier.fulfilled) != 1 || len(notifier.fulfilled[0]) != 2 {
		t.Fatalf("notifier.ReturnRequestsFulfilled = %v, want exactly one call with two notifications", notifier.fulfilled)
	}
}

// TestOperationService_ReturnToBin_NoOpenRequestsNeverNotifies proves the
// common case costs nothing extra: an item with no open requests fulfils
// none and never calls the notifier.
func TestOperationService_ReturnToBin_NoOpenRequestsNeverNotifies(t *testing.T) {
	holder := identity.NewUserID()
	store, bins := heldFixture(holder)
	destBinID := domain.NewBinID()
	bins.bin = &domain.Bin{ID: destBinID}
	svc, _, _, notifier := newTestOperationServiceWithReturnRequests(store, bins, nil, nil)
	actor := identity.NewUserPrincipal(holder, identity.RoleAdult, "Alice")

	op, err := svc.ReturnToBin(context.Background(), actor, store.item.ID, destBinID, nil)
	if err != nil {
		t.Fatalf("ReturnToBin: %v", err)
	}
	if len(op.FulfilledReturnRequests) != 0 {
		t.Errorf("Operation.FulfilledReturnRequests = %v, want none", op.FulfilledReturnRequests)
	}
	if len(notifier.fulfilled) != 0 {
		t.Error("notifier.ReturnRequestsFulfilled must not be called when nothing was fulfilled")
	}
}

// TestOperationService_RemoveFromBin_NeverFulfils proves NSTR-43's own
// held-to-bin rule the other direction: RemoveFromBin's destination is a
// holder, never a bin, so it can never fulfil a return request even though
// the fake fulfiller has one seeded against the same item — a fulfiller
// that fired here would be a straightforward regression to guard against.
func TestOperationService_RemoveFromBin_NeverFulfils(t *testing.T) {
	store, bins, _ := binnedFixture()
	requester := identity.NewUserID()
	open := openReturnRequestOn(store.item.ID, requester, identity.NewUserID())
	svc, _, _, notifier := newTestOperationServiceWithReturnRequests(store, bins, []domain.ReturnRequest{open}, nil)
	holder := identity.NewUserID()
	actor := identity.NewUserPrincipal(holder, identity.RoleAdult, "Bob")

	op, err := svc.RemoveFromBin(context.Background(), actor, store.item.ID, nil)
	if err != nil {
		t.Fatalf("RemoveFromBin: %v", err)
	}
	if len(op.FulfilledReturnRequests) != 0 {
		t.Errorf("RemoveFromBin fulfilled %v, want none", op.FulfilledReturnRequests)
	}
	if len(notifier.fulfilled) != 0 {
		t.Error("RemoveFromBin must never call ReturnRequestsFulfilled")
	}
}

// TestOperationService_ReturnToBin_FulfillFailureAbortsOperation proves
// fulfilment is inside the SAME transaction as the placement swap and its
// event: a failing FulfillOpenForItem rolls back the whole operation,
// exactly as a failing EventAppender already does
// (TestOperationService_RemoveFromBin_EventAppendFailureAbortsOperation).
func TestOperationService_ReturnToBin_FulfillFailureAbortsOperation(t *testing.T) {
	holder := identity.NewUserID()
	store, bins := heldFixture(holder)
	destBinID := domain.NewBinID()
	bins.bin = &domain.Bin{ID: destBinID}
	events := &fakeEventAppender{}
	requests := &fakeReturnRequestFulfiller{fulfillErr: errors.New("boom")}
	uow := &fakeUnitOfWork{store: store, events: events, requests: requests}
	notifier := &fakeReturnRequestNotifier{}
	svc := app.NewOperationService(uow, bins, &fakeUserLabelResolver{}, notifier, testLogger())
	actor := identity.NewUserPrincipal(holder, identity.RoleAdult, "Alice")

	_, err := svc.ReturnToBin(context.Background(), actor, store.item.ID, destBinID, nil)
	if err == nil {
		t.Fatal("ReturnToBin with a failing FulfillOpenForItem = nil error, want one")
	}
	if store.item.HeldBy == nil || *store.item.HeldBy != holder {
		t.Errorf("a failing FulfillOpenForItem must roll back the placement swap too: HeldBy = %v, want unchanged %v", store.item.HeldBy, holder)
	}
	if len(events.events) != 0 {
		t.Error("a failing FulfillOpenForItem must leave no event committed")
	}
	if len(notifier.fulfilled) != 0 {
		t.Error("a rolled-back operation must never notify")
	}
}

// TestOperationService_AddToBin_NotifierErrorNeverFailsOperation proves the
// notifier is genuinely best-effort: OperationService.notifyFulfilled
// itself never returns an error for a caller to check, so there is no
// return value to assert on here — this test's real assertion is simply
// that AddToBin succeeds and reports the fulfilled request even when its
// requester cannot be resolved to a label (a fakeUserLabelResolver with an
// empty user map), never surfacing that lookup failure as an operation
// error.
func TestOperationService_AddToBin_NotifierErrorNeverFailsOperation(t *testing.T) {
	holder := identity.NewUserID()
	store, bins := heldFixture(holder)
	destBinID := domain.NewBinID()
	bins.bin = &domain.Bin{ID: destBinID, Code: "B2", Name: "Bin B"}
	requester := identity.NewUserID()
	open := openReturnRequestOn(store.item.ID, requester, holder)
	svc, _, _, notifier := newTestOperationServiceWithReturnRequests(store, bins, []domain.ReturnRequest{open}, nil)
	actor := identity.NewUserPrincipal(holder, identity.RoleAdult, "Alice")

	op, err := svc.AddToBin(context.Background(), actor, store.item.ID, destBinID)
	if err != nil {
		t.Fatalf("AddToBin: %v", err)
	}
	if len(op.FulfilledReturnRequests) != 1 {
		t.Fatalf("Operation.FulfilledReturnRequests = %v, want exactly 1", op.FulfilledReturnRequests)
	}
	if len(notifier.fulfilled) != 1 || notifier.fulfilled[0][0].RequesterLabel != "" {
		t.Errorf("notification = %v, want exactly one call with an empty RequesterLabel (unresolved requester)", notifier.fulfilled)
	}
}
