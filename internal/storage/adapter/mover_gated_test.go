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

// newBinMover wires an app.BinMover against real Postgres via f's own pool:
// a PostgresUnitOfWork for the transactional GetForUpdate/Move pair the move
// runs through, and f.bins/f.locations directly (BinMover dependencies, not
// tx-scoped — visibility checks need no row lock of their own). Mirrors
// newOperationService's own wiring rationale (operations_gated_test.go).
func newBinMover(f *itemFixture) *app.BinMover {
	uow := adapter.NewPostgresUnitOfWork(f.pool)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return app.NewBinMover(uow, f.bins, f.locations, time.Now, logger)
}

func TestBinRepository_GetForUpdate(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	binID := f.seedBin(t, creator, loc, domain.VisibilityPublic)

	got, err := f.bins.GetForUpdate(testCtx(t), binID)
	if err != nil {
		t.Fatalf("GetForUpdate: %v", err)
	}
	if got.ID != binID || got.LocationID != loc {
		t.Errorf("GetForUpdate = %+v, want id=%v location=%v", got, binID, loc)
	}
}

func TestBinRepository_GetForUpdate_NotFound(t *testing.T) {
	f := newItemFixture(t)
	_, err := f.bins.GetForUpdate(testCtx(t), domain.NewBinID())
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("GetForUpdate(unknown) = %v, want ErrBinNotFound", err)
	}
}

func TestBinRepository_Move(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	from := f.seedLocation(t, creator)
	to := f.seedLocation(t, creator)
	binID := f.seedBin(t, creator, from, domain.VisibilityPublic)

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	before, err := f.bins.FindVisibleByID(testCtx(t), viewer, binID)
	if err != nil {
		t.Fatalf("FindVisibleByID before Move: %v", err)
	}

	// Truncated to microsecond resolution — Postgres timestamptz's own
	// precision — so the round trip compares equal, matching the identity
	// adapter's own gated tests (e.g. api_key_postgres_gated_test.go).
	now := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	affected, err := f.bins.Move(testCtx(t), binID, to, now)
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if affected != 1 {
		t.Errorf("Move rowsAffected = %d, want 1", affected)
	}

	got, err := f.bins.FindVisibleByID(testCtx(t), viewer, binID)
	if err != nil {
		t.Fatalf("FindVisibleByID after Move: %v", err)
	}
	if got.LocationID != to {
		t.Errorf("LocationID after Move = %v, want %v", got.LocationID, to)
	}
	if !got.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("UpdatedAt after Move = %v, want strictly after %v", got.UpdatedAt, before.UpdatedAt)
	}
	if !got.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt after Move = %v, want exactly the supplied now %v", got.UpdatedAt, now)
	}
}

func TestBinRepository_Move_NotFound(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)

	_, err := f.bins.Move(testCtx(t), domain.NewBinID(), loc, time.Now())
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("Move(unknown bin) = %v, want ErrBinNotFound", err)
	}
}

func TestBinRepository_Move_UnknownLocationRejected(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	binID := f.seedBin(t, creator, loc, domain.VisibilityPublic)

	_, err := f.bins.Move(testCtx(t), binID, domain.NewLocationID(), time.Now())
	if !errors.Is(err, domain.ErrLocationNotFound) {
		t.Errorf("Move(unknown location) = %v, want ErrLocationNotFound", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.bins.FindVisibleByID(testCtx(t), viewer, binID)
	if err != nil {
		t.Fatalf("FindVisibleByID after rejected Move: %v", err)
	}
	if got.LocationID != loc {
		t.Error("Move to an unknown location must leave the bin's own location unchanged")
	}
}

// TestBinMover_Move_RelocatesBin is the ticket's own headline acceptance
// criterion: moving a bin updates its location.
func TestBinMover_Move_RelocatesBin(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	from := f.seedLocation(t, creator)
	to := f.seedLocation(t, creator)
	binID := f.seedBin(t, creator, from, domain.VisibilityPublic)

	mover := newBinMover(f)
	actor := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")

	result, err := mover.Move(testCtx(t), actor, binID, to)
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if result.BinID != binID || result.FromLocationID != from || result.ToLocationID != to || result.MovedBy != creator {
		t.Errorf("MoveResult = %+v, want bin=%v from=%v to=%v movedBy=%v", result, binID, from, to, creator)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.bins.FindVisibleByID(testCtx(t), viewer, binID)
	if err != nil {
		t.Fatalf("FindVisibleByID after Move: %v", err)
	}
	if got.LocationID != to {
		t.Errorf("LocationID after Move = %v, want %v", got.LocationID, to)
	}
}

// TestBinMover_Move_NoopRejected proves the ticket's no-op acceptance
// criterion end to end through the real service and database.
func TestBinMover_Move_NoopRejected(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	binID := f.seedBin(t, creator, loc, domain.VisibilityPublic)

	mover := newBinMover(f)
	actor := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")

	_, err := mover.Move(testCtx(t), actor, binID, loc)
	if !errors.Is(err, domain.ErrBinAlreadyInLocation) {
		t.Errorf("Move(current location) = %v, want ErrBinAlreadyInLocation", err)
	}
}

// TestBinMover_Move_UnknownLocationRejected proves the ticket's "nonexistent
// or invisible location" acceptance criterion. Location carries no privacy
// field (unlike Bin's Visibility — see LocationRepository.FindVisibleByID's
// own doc), so every location a viewer could name either exists (and is
// visible) or does not exist at all: "invisible" and "nonexistent" collapse
// to the same case today, and this is that case's proof.
func TestBinMover_Move_UnknownLocationRejected(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	binID := f.seedBin(t, creator, loc, domain.VisibilityPublic)

	mover := newBinMover(f)
	actor := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")

	_, err := mover.Move(testCtx(t), actor, binID, domain.NewLocationID())
	if !errors.Is(err, domain.ErrLocationNotFound) {
		t.Errorf("Move(unknown location) = %v, want ErrLocationNotFound", err)
	}
}

// TestBinMover_Move_InvisiblePrivateBinRejected proves the sprint-level
// decision's "existence alone is insufficient" requirement on the bin side,
// where it is real: a private bin is present in the database but invisible
// to a non-creator, non-admin viewer, and Move must reject it exactly like a
// missing bin (ErrBinNotFound), never leaking that it exists.
func TestBinMover_Move_InvisiblePrivateBinRejected(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	other := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	to := f.seedLocation(t, creator)
	binID := f.seedBin(t, creator, loc, domain.VisibilityPrivate)

	mover := newBinMover(f)
	actor := identity.NewUserPrincipal(other, identity.RoleMember, "Other")

	_, err := mover.Move(testCtx(t), actor, binID, to)
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("Move(invisible private bin) = %v, want ErrBinNotFound", err)
	}

	creatorViewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.bins.FindVisibleByID(testCtx(t), creatorViewer, binID)
	if err != nil {
		t.Fatalf("FindVisibleByID after rejected Move: %v", err)
	}
	if got.LocationID != loc {
		t.Error("Move rejected for invisibility must leave the bin's own location unchanged")
	}
}

// TestBinMover_Move_ItemsUnaffectedByMove proves the ticket's "item counts
// and contents are unchanged by a move" acceptance criterion: items carry
// bin_id, not location_id, so relocating the bin never touches the item
// table at all.
func TestBinMover_Move_ItemsUnaffectedByMove(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	from := f.seedLocation(t, creator)
	to := f.seedLocation(t, creator)
	binID := f.seedBin(t, creator, from, domain.VisibilityPublic)
	it := newItem("Camping stove", binID, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create(item): %v", err)
	}
	beforePlacementChange := it.PlacementChangedAt

	mover := newBinMover(f)
	actor := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")

	if _, err := mover.Move(testCtx(t), actor, binID, to); err != nil {
		t.Fatalf("Move: %v", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	items, err := f.repo.ListByBin(testCtx(t), viewer, binID)
	if err != nil {
		t.Fatalf("ListByBin after Move: %v", err)
	}
	if len(items) != 1 || items[0].ID != it.ID {
		t.Fatalf("ListByBin after Move = %d items, want exactly the one item still in the bin", len(items))
	}
	if items[0].CurrentBinID == nil || *items[0].CurrentBinID != binID {
		t.Errorf("item CurrentBinID after bin Move = %v, want unchanged %v", items[0].CurrentBinID, binID)
	}
	if !items[0].PlacementChangedAt.Equal(beforePlacementChange) {
		t.Errorf("bin Move must not advance the item's PlacementChangedAt: got %v, want unchanged %v",
			items[0].PlacementChangedAt, beforePlacementChange)
	}
}

// TestBinMover_Move_ConcurrentAttemptsOnlyOneWins is the ticket's own
// required concurrency proof, mirroring
// TestOperationService_RemoveFromBin_ConcurrentAttemptsOnlyOneWins: two
// goroutines move the same bin to the same target at once. FOR UPDATE row
// locking (GetForUpdate, held for the whole transaction) serializes them —
// the second blocks until the first commits, then re-reads the now-relocated
// bin and fails MoveTo's own no-op guard — so exactly one succeeds and the
// other fails with ErrBinAlreadyInLocation, never both (and never a bin left
// in neither the original nor the target location).
func TestBinMover_Move_ConcurrentAttemptsOnlyOneWins(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	from := f.seedLocation(t, creator)
	to := f.seedLocation(t, creator)
	binID := f.seedBin(t, creator, from, domain.VisibilityPublic)

	mover := newBinMover(f)
	actorA := identity.NewUserPrincipal(f.seedUser(t, identity.RoleMember), identity.RoleMember, "A")
	actorB := identity.NewUserPrincipal(f.seedUser(t, identity.RoleMember), identity.RoleMember, "B")

	var wg sync.WaitGroup
	results := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, results[0] = mover.Move(context.Background(), actorA, binID, to)
	}()
	go func() {
		defer wg.Done()
		_, results[1] = mover.Move(context.Background(), actorB, binID, to)
	}()
	wg.Wait()

	succeeded, failed := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, domain.ErrBinAlreadyInLocation):
			failed++
		default:
			t.Errorf("unexpected error from concurrent Move: %v", err)
		}
	}
	if succeeded != 1 || failed != 1 {
		t.Errorf("concurrent Move: succeeded=%d failed=%d, want exactly one of each", succeeded, failed)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.bins.FindVisibleByID(testCtx(t), viewer, binID)
	if err != nil {
		t.Fatalf("FindVisibleByID after concurrent Move: %v", err)
	}
	if got.LocationID != to {
		t.Errorf("after concurrent Move, LocationID = %v, want %v", got.LocationID, to)
	}
}
