package adapter_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/adapter"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// testOpsLogger discards output — these tests assert on returned/persisted
// values, not log lines, mirroring testLogger's counterparts in the app
// package's hermetic tests.
func testOpsLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// newOperationService wires an app.OperationService against real Postgres
// via f's own pool and BinRepository: a PostgresUnitOfWork for the
// transactional GetForUpdate/Move pair these operations run through, and
// f.bins directly (an OperationService dependency, not tx-scoped — bin
// visibility needs no row lock of its own). The same wiring
// cmd/server/main.go will do once NSTR-31 exposes this service through a
// handler.
func newOperationService(f *itemFixture) *app.OperationService {
	uow := adapter.NewPostgresUnitOfWork(f.pool)
	return app.NewOperationService(uow, f.bins, testOpsLogger())
}

func TestOperationService_AddToBin_MovesHeldItemIntoBin(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	holder := f.seedUser(t, identity.RoleMember)
	it := &domain.Item{ID: domain.NewItemID(), Name: "Stove", Quantity: 1, HeldBy: &holder, CreatedBy: creator}
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}
	beforeAdd := it.PlacementChangedAt

	svc := newOperationService(f)
	actor := identity.NewUserPrincipal(holder, identity.RoleMember, "Holder")

	time.Sleep(2 * time.Millisecond)
	op, err := svc.AddToBin(testCtx(t), actor, it.ID, bin)
	if err != nil {
		t.Fatalf("AddToBin: %v", err)
	}
	if op.Item.CurrentBinID == nil || *op.Item.CurrentBinID != bin {
		t.Errorf("AddToBin: CurrentBinID = %v, want %v", op.Item.CurrentBinID, bin)
	}
	if op.Item.HeldBy != nil {
		t.Error("AddToBin must clear HeldBy")
	}
	if !op.Item.PlacementChangedAt.After(beforeAdd) {
		t.Errorf("AddToBin: PlacementChangedAt = %v, want strictly after %v", op.Item.PlacementChangedAt, beforeAdd)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.repo.Get(testCtx(t), viewer, it.ID)
	if err != nil {
		t.Fatalf("Get after AddToBin: %v", err)
	}
	if got.CurrentBinID == nil || *got.CurrentBinID != bin {
		t.Errorf("Get after AddToBin: CurrentBinID = %v, want %v", got.CurrentBinID, bin)
	}
	if !got.PlacementChangedAt.After(beforeAdd) {
		t.Errorf("persisted PlacementChangedAt = %v, want strictly after %v", got.PlacementChangedAt, beforeAdd)
	}
}

func TestOperationService_AddToBin_AlreadyInBinRejected_NoPartialWrite(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	binA := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	binB := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem("Stove", binA, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}
	beforeAttempt := it.PlacementChangedAt

	svc := newOperationService(f)
	actor := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")

	_, err := svc.AddToBin(testCtx(t), actor, it.ID, binB)
	if !errors.Is(err, domain.ErrItemAlreadyInBin) {
		t.Errorf("AddToBin(already in bin) = %v, want ErrItemAlreadyInBin", err)
	}

	got, err := f.repo.Get(testCtx(t), actor, it.ID)
	if err != nil {
		t.Fatalf("Get after rejected AddToBin: %v", err)
	}
	if got.CurrentBinID == nil || *got.CurrentBinID != binA {
		t.Error("rejected AddToBin must not move the item")
	}
	if !got.PlacementChangedAt.Equal(beforeAttempt) {
		t.Errorf("rejected AddToBin must not advance PlacementChangedAt: got %v, want unchanged %v", got.PlacementChangedAt, beforeAttempt)
	}
}

func TestOperationService_RemoveFromBin_ChecksOutBinnedItem(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem("Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}
	beforeRemove := it.PlacementChangedAt

	svc := newOperationService(f)
	holder := f.seedUser(t, identity.RoleMember)
	actor := identity.NewUserPrincipal(holder, identity.RoleMember, "Holder")

	time.Sleep(2 * time.Millisecond)
	op, err := svc.RemoveFromBin(testCtx(t), actor, it.ID)
	if err != nil {
		t.Fatalf("RemoveFromBin: %v", err)
	}
	if op.Item.HeldBy == nil || *op.Item.HeldBy != holder {
		t.Errorf("RemoveFromBin: HeldBy = %v, want %v", op.Item.HeldBy, holder)
	}
	if op.Item.CurrentBinID != nil {
		t.Error("RemoveFromBin must clear CurrentBinID")
	}
	if !op.Item.PlacementChangedAt.After(beforeRemove) {
		t.Errorf("RemoveFromBin: PlacementChangedAt = %v, want strictly after %v", op.Item.PlacementChangedAt, beforeRemove)
	}
}

func TestOperationService_RemoveFromBin_IntegrationPrincipalRejected(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem("Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	svc := newOperationService(f)
	actor := identity.NewIntegrationPrincipal("Nestova")

	_, err := svc.RemoveFromBin(testCtx(t), actor, it.ID)
	if !errors.Is(err, domain.ErrHolderRequired) {
		t.Errorf("RemoveFromBin(integration principal) = %v, want ErrHolderRequired", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.repo.Get(testCtx(t), viewer, it.ID)
	if err != nil {
		t.Fatalf("Get after rejected RemoveFromBin: %v", err)
	}
	if got.CurrentBinID == nil || *got.CurrentBinID != bin {
		t.Error("rejected RemoveFromBin must not touch the item")
	}
}

func TestOperationService_ReturnToBin_ReturnsHeldItem(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	holder := f.seedUser(t, identity.RoleMember)
	it := &domain.Item{ID: domain.NewItemID(), Name: "Stove", Quantity: 1, HeldBy: &holder, CreatedBy: creator}
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}
	beforeReturn := it.PlacementChangedAt

	svc := newOperationService(f)
	actor := identity.NewUserPrincipal(holder, identity.RoleMember, "Holder")

	time.Sleep(2 * time.Millisecond)
	op, err := svc.ReturnToBin(testCtx(t), actor, it.ID, bin)
	if err != nil {
		t.Fatalf("ReturnToBin: %v", err)
	}
	if op.Item.CurrentBinID == nil || *op.Item.CurrentBinID != bin {
		t.Errorf("ReturnToBin: CurrentBinID = %v, want %v", op.Item.CurrentBinID, bin)
	}
	if op.Item.HeldBy != nil {
		t.Error("ReturnToBin must clear HeldBy")
	}
	if !op.Item.PlacementChangedAt.After(beforeReturn) {
		t.Errorf("ReturnToBin: PlacementChangedAt = %v, want strictly after %v", op.Item.PlacementChangedAt, beforeReturn)
	}
}

func TestOperationService_ReturnToBin_NotCheckedOutRejected(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	binA := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	binB := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem("Stove", binA, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	svc := newOperationService(f)
	actor := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")

	_, err := svc.ReturnToBin(testCtx(t), actor, it.ID, binB)
	if !errors.Is(err, domain.ErrItemNotCheckedOut) {
		t.Errorf("ReturnToBin(not checked out) = %v, want ErrItemNotCheckedOut", err)
	}
}

// TestOperationService_Edit_DoesNotAdvancePlacementChangedAt proves the
// sprint-level decision's negative case: a plain field edit (name/
// description/quantity, via ItemRepository.Update — the same path
// ItemService.Edit calls) must never touch PlacementChangedAt, only a
// placement swap through OperationService may. NSTR-28's own
// TestItemRepository_Update already asserts this from the repository side;
// this test re-asserts it from the operations suite so the "not by a plain
// edit" half of the sprint decision has a home next to its "advances on
// every op" siblings above.
func TestOperationService_Edit_DoesNotAdvancePlacementChangedAt(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem("Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}
	beforeEdit := it.PlacementChangedAt

	time.Sleep(2 * time.Millisecond)
	desc := "Two-burner camping stove"
	update := &domain.Item{ID: it.ID, Name: "Camping stove", Description: &desc, Quantity: 2}
	if err := f.repo.Update(testCtx(t), update); err != nil {
		t.Fatalf("Update: %v", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.repo.Get(testCtx(t), viewer, it.ID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if !got.PlacementChangedAt.Equal(beforeEdit) {
		t.Errorf("plain edit must not advance PlacementChangedAt: got %v, want unchanged %v", got.PlacementChangedAt, beforeEdit)
	}
}

// TestOperationService_RemoveFromBin_ConcurrentAttemptsOnlyOneWins is the
// ticket's own required concurrency proof: two goroutines call
// RemoveFromBin on the same binned item at once. FOR UPDATE row locking
// (GetForUpdate, held for the whole transaction) serializes them — the
// second blocks until the first commits, then re-reads the now-held item
// and fails CheckOut's guard — so exactly one succeeds, the other fails
// with ErrItemAlreadyCheckedOut, and the item never ends up with neither or
// both of current_bin_id/held_by set (the database CHECK's own guarantee,
// exercised here under real concurrency rather than only sequentially).
func TestOperationService_RemoveFromBin_ConcurrentAttemptsOnlyOneWins(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem("Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	svc := newOperationService(f)
	holderA := f.seedUser(t, identity.RoleMember)
	holderB := f.seedUser(t, identity.RoleMember)
	actorA := identity.NewUserPrincipal(holderA, identity.RoleMember, "A")
	actorB := identity.NewUserPrincipal(holderB, identity.RoleMember, "B")

	var wg sync.WaitGroup
	results := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, results[0] = svc.RemoveFromBin(context.Background(), actorA, it.ID)
	}()
	go func() {
		defer wg.Done()
		_, results[1] = svc.RemoveFromBin(context.Background(), actorB, it.ID)
	}()
	wg.Wait()

	succeeded, failed := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, domain.ErrItemAlreadyCheckedOut):
			failed++
		default:
			t.Errorf("unexpected error from concurrent RemoveFromBin: %v", err)
		}
	}
	if succeeded != 1 || failed != 1 {
		t.Errorf("concurrent RemoveFromBin: succeeded=%d failed=%d, want exactly one of each", succeeded, failed)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.repo.Get(testCtx(t), viewer, it.ID)
	if err != nil {
		t.Fatalf("Get after concurrent RemoveFromBin: %v", err)
	}
	if got.CurrentBinID != nil {
		t.Error("after concurrent RemoveFromBin, the item must not remain in a bin (no partial write)")
	}
	if got.HeldBy == nil || (*got.HeldBy != holderA && *got.HeldBy != holderB) {
		t.Errorf("after concurrent RemoveFromBin, HeldBy = %v, want exactly one of %v/%v", got.HeldBy, holderA, holderB)
	}
}

func TestNewPostgresUnitOfWork_NilPoolPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewPostgresUnitOfWork(nil) did not panic")
		}
	}()
	adapter.NewPostgresUnitOfWork(nil)
}
