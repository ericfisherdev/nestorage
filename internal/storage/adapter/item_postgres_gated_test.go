package adapter_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
	"github.com/ericfisherdev/nestorage/internal/storage/adapter"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// itemFixture wires an ItemRepository alongside the Bin/Location/User
// repositories item's foreign keys require, over ONE derived database —
// this shares locationFixture's and binFixture's "storage" suffix (the
// suffix is per *package*, not per aggregate; see binFixture's own doc for
// why splitting it would defeat the harness's isolation).
type itemFixture struct {
	pool      *pgxpool.Pool
	repo      *adapter.ItemRepository
	bins      *adapter.BinRepository
	locations *adapter.LocationRepository
	users     *identityadapter.UserRepository
}

func newItemFixture(t *testing.T) *itemFixture {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "storage")
	return &itemFixture{
		pool:      pool,
		repo:      adapter.NewItemRepository(pool),
		bins:      adapter.NewBinRepository(pool),
		locations: adapter.NewLocationRepository(pool),
		users:     identityadapter.NewUserRepository(pool),
	}
}

// seedUser creates and returns the id of a user with the given role, for
// item's held_by/created_by FKs and for building viewer principals.
func (f *itemFixture) seedUser(t *testing.T, role identity.Role) identity.UserID {
	t.Helper()
	u := &identity.User{
		ID:           identity.NewUserID(),
		DisplayName:  "Test User",
		Email:        "item-user-" + identity.NewUserID().String() + "@example.com",
		PasswordHash: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		Role:         role,
		Color:        identity.ColorIndigo,
	}
	if err := f.users.Create(testCtx(t), u); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u.ID
}

// seedLocation creates and returns the id of a location for bin's
// location_id FK.
func (f *itemFixture) seedLocation(t *testing.T, createdBy identity.UserID) domain.LocationID {
	t.Helper()
	loc := &domain.Location{ID: domain.NewLocationID(), Name: "Garage", CreatedBy: createdBy}
	if err := f.locations.Create(testCtx(t), loc); err != nil {
		t.Fatalf("seed location: %v", err)
	}
	return loc.ID
}

// seedBin creates and returns the id of a bin with the given visibility,
// for item's current_bin_id FK and for exercising the visibility matrix.
func (f *itemFixture) seedBin(t *testing.T, createdBy identity.UserID, location domain.LocationID, visibility domain.Visibility) domain.BinID {
	t.Helper()
	id := domain.NewBinID()
	b := &domain.Bin{
		// Code is derived from the bin's own id (rather than a second,
		// independently generated NewBinID) so it is guaranteed unique per
		// call: two UUIDv7s minted within the same millisecond share their
		// timestamp-derived prefix, which collided when this used a
		// separately generated id's leading characters.
		ID:         id,
		Code:       "ITM" + id.String(),
		Name:       "Item bin",
		LocationID: location,
		CreatedBy:  createdBy,
		Visibility: visibility,
	}
	if err := f.bins.Create(testCtx(t), b); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
	return b.ID
}

func newItem(name string, binID domain.BinID, createdBy identity.UserID) *domain.Item {
	return &domain.Item{
		ID:           domain.NewItemID(),
		Name:         name,
		Quantity:     1,
		CurrentBinID: &binID,
		CreatedBy:    createdBy,
	}
}

func TestItemRepository_CreateAndGet(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	desc := "Two-burner camping stove"
	it := newItem("Camping stove", bin, creator)
	it.Description = &desc
	it.Quantity = 3

	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if it.CreatedAt.IsZero() || it.UpdatedAt.IsZero() || it.PlacementChangedAt.IsZero() {
		t.Error("Create left a timestamp zero")
	}
	if !it.PlacementChangedAt.Equal(it.CreatedAt) {
		t.Errorf("Create: PlacementChangedAt = %v, want it to equal CreatedAt (%v)", it.PlacementChangedAt, it.CreatedAt)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.repo.Get(testCtx(t), viewer, it.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "Camping stove" || got.Description == nil || *got.Description != desc || got.Quantity != 3 {
		t.Errorf("Get = %+v, want it to match the created item", got)
	}
	if got.CurrentBinID == nil || *got.CurrentBinID != bin {
		t.Errorf("Get.CurrentBinID = %v, want %v", got.CurrentBinID, bin)
	}
	if got.HeldBy != nil {
		t.Errorf("Get.HeldBy = %v, want nil for an in-bin item", got.HeldBy)
	}
	if got.CreatedBy != creator {
		t.Errorf("Get.CreatedBy = %v, want %v", got.CreatedBy, creator)
	}
}

func TestItemRepository_Create_HeldByRoundTrips(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	holder := f.seedUser(t, identity.RoleMember)
	it := &domain.Item{ID: domain.NewItemID(), Name: "Sleeping bag", Quantity: 1, HeldBy: &holder, CreatedBy: creator}

	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A held item is ungated: even an unrelated non-creator, non-holder
	// member can see it, since it has no bin to gate on.
	other := f.seedUser(t, identity.RoleMember)
	otherViewer := identity.NewUserPrincipal(other, identity.RoleMember, "Other")
	got, err := f.repo.Get(testCtx(t), otherViewer, it.ID)
	if err != nil {
		t.Fatalf("Get(unrelated viewer, held item): %v", err)
	}
	if got.CurrentBinID != nil {
		t.Errorf("Get.CurrentBinID = %v, want nil for a held item", got.CurrentBinID)
	}
	if got.HeldBy == nil || *got.HeldBy != holder {
		t.Errorf("Get.HeldBy = %v, want %v", got.HeldBy, holder)
	}
}

func TestItemRepository_Create_QuantityRejected(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)

	it := newItem("Broken quantity", bin, creator)
	it.Quantity = 0

	err := f.repo.Create(testCtx(t), it)
	if !errors.Is(err, domain.ErrInvalidQuantity) {
		t.Errorf("Create(quantity=0) = %v, want ErrInvalidQuantity", err)
	}
}

func TestItemRepository_Create_PlacementRejected(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)

	t.Run("both bin and holder", func(t *testing.T) {
		it := newItem("Both", bin, creator)
		it.HeldBy = &creator
		err := f.repo.Create(testCtx(t), it)
		if !errors.Is(err, domain.ErrInvalidPlacement) {
			t.Errorf("Create(bin and holder) = %v, want ErrInvalidPlacement", err)
		}
	})

	t.Run("neither bin nor holder", func(t *testing.T) {
		it := &domain.Item{ID: domain.NewItemID(), Name: "Neither", Quantity: 1, CreatedBy: creator}
		err := f.repo.Create(testCtx(t), it)
		if !errors.Is(err, domain.ErrInvalidPlacement) {
			t.Errorf("Create(neither) = %v, want ErrInvalidPlacement", err)
		}
	})
}

func TestItemRepository_Create_UnknownBinRejected(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	it := newItem("Ghost bin", domain.NewBinID(), creator)

	err := f.repo.Create(testCtx(t), it)
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("Create(unknown bin) = %v, want ErrBinNotFound", err)
	}
}

func TestItemRepository_Create_UnknownHeldByRejected(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	ghost := identity.NewUserID()
	it := &domain.Item{ID: domain.NewItemID(), Name: "Ghost holder", Quantity: 1, HeldBy: &ghost, CreatedBy: creator}

	err := f.repo.Create(testCtx(t), it)
	if !errors.Is(err, identity.ErrUserNotFound) {
		t.Errorf("Create(unknown held_by) = %v, want identity.ErrUserNotFound", err)
	}
}

func TestItemRepository_Create_UnknownCreatedByRejected(t *testing.T) {
	f := newItemFixture(t)
	admin := f.seedUser(t, identity.RoleAdmin)
	loc := f.seedLocation(t, admin)
	bin := f.seedBin(t, admin, loc, domain.VisibilityPublic)
	it := newItem("Ghost creator", bin, identity.NewUserID())

	err := f.repo.Create(testCtx(t), it)
	if !errors.Is(err, identity.ErrUserNotFound) {
		t.Errorf("Create(unknown created_by) = %v, want identity.ErrUserNotFound", err)
	}
}

func TestItemRepository_Get_NotFound(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")

	_, err := f.repo.Get(testCtx(t), viewer, domain.NewItemID())
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Get(unknown) = %v, want ErrItemNotFound", err)
	}
}

func TestItemRepository_Update(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem("Old name", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}
	originalPlacementChangedAt := it.PlacementChangedAt

	desc := "Updated description"
	update := &domain.Item{ID: it.ID, Name: "New name", Description: &desc, Quantity: 5}
	if err := f.repo.Update(testCtx(t), update); err != nil {
		t.Fatalf("Update: %v", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.repo.Get(testCtx(t), viewer, it.ID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if got.Name != "New name" || got.Description == nil || *got.Description != desc || got.Quantity != 5 {
		t.Errorf("Get after Update = %+v, want the new name/description/quantity", got)
	}
	if got.CurrentBinID == nil || *got.CurrentBinID != bin {
		t.Error("Update must never touch placement")
	}
	if !got.PlacementChangedAt.Equal(originalPlacementChangedAt) {
		t.Errorf("Update changed PlacementChangedAt from %v to %v, want unchanged", originalPlacementChangedAt, got.PlacementChangedAt)
	}
}

func TestItemRepository_Update_QuantityRejected(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem("Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	update := &domain.Item{ID: it.ID, Name: "Stove", Quantity: -1}
	err := f.repo.Update(testCtx(t), update)
	if !errors.Is(err, domain.ErrInvalidQuantity) {
		t.Errorf("Update(quantity=-1) = %v, want ErrInvalidQuantity", err)
	}
}

func TestItemRepository_Update_NotFound(t *testing.T) {
	f := newItemFixture(t)
	update := &domain.Item{ID: domain.NewItemID(), Name: "Ghost", Quantity: 1}
	err := f.repo.Update(testCtx(t), update)
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Update(unknown) = %v, want ErrItemNotFound", err)
	}
}

// TestItemRepository_Move_BinToHolderToBin exercises the full placement
// primitive round trip the AC's "repository tests cover ... move" asks for,
// asserting rows-affected and that PlacementChangedAt strictly advances on
// each move (unlike a plain Update, see TestItemRepository_Update's own
// assertion that Update leaves it untouched).
func TestItemRepository_Move_BinToHolderToBin(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	holder := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	binA := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	binB := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem("Traveling item", binA, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}
	afterCreate := it.PlacementChangedAt

	// Postgres timestamptz has microsecond resolution; a short sleep keeps
	// each move's now() strictly after the previous one on fast hardware.
	time.Sleep(2 * time.Millisecond)
	affected, err := f.repo.Move(testCtx(t), it.ID, domain.PlacementHeldBy(holder))
	if err != nil {
		t.Fatalf("Move(to holder): %v", err)
	}
	if affected != 1 {
		t.Errorf("Move(to holder) rows affected = %d, want 1", affected)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.repo.Get(testCtx(t), viewer, it.ID)
	if err != nil {
		t.Fatalf("Get after Move(to holder): %v", err)
	}
	if got.CurrentBinID != nil {
		t.Errorf("after Move(to holder), CurrentBinID = %v, want nil", got.CurrentBinID)
	}
	if got.HeldBy == nil || *got.HeldBy != holder {
		t.Errorf("after Move(to holder), HeldBy = %v, want %v", got.HeldBy, holder)
	}
	if !got.PlacementChangedAt.After(afterCreate) {
		t.Errorf("Move(to holder) PlacementChangedAt = %v, want strictly after %v", got.PlacementChangedAt, afterCreate)
	}
	afterHeld := got.PlacementChangedAt

	time.Sleep(2 * time.Millisecond)
	affected, err = f.repo.Move(testCtx(t), it.ID, domain.PlacementInBin(binB))
	if err != nil {
		t.Fatalf("Move(to binB): %v", err)
	}
	if affected != 1 {
		t.Errorf("Move(to binB) rows affected = %d, want 1", affected)
	}

	got, err = f.repo.Get(testCtx(t), viewer, it.ID)
	if err != nil {
		t.Fatalf("Get after Move(to binB): %v", err)
	}
	if got.HeldBy != nil {
		t.Errorf("after Move(to binB), HeldBy = %v, want nil", got.HeldBy)
	}
	if got.CurrentBinID == nil || *got.CurrentBinID != binB {
		t.Errorf("after Move(to binB), CurrentBinID = %v, want %v", got.CurrentBinID, binB)
	}
	if !got.PlacementChangedAt.After(afterHeld) {
		t.Errorf("Move(to binB) PlacementChangedAt = %v, want strictly after %v", got.PlacementChangedAt, afterHeld)
	}
}

func TestItemRepository_Move_NotFound(t *testing.T) {
	f := newItemFixture(t)
	holder := f.seedUser(t, identity.RoleMember)

	affected, err := f.repo.Move(testCtx(t), domain.NewItemID(), domain.PlacementHeldBy(holder))
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Move(unknown) = %v, want ErrItemNotFound", err)
	}
	if affected != 0 {
		t.Errorf("Move(unknown) rows affected = %d, want 0", affected)
	}
}

func TestItemRepository_Move_InvalidPlacementRejected(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem("Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	affected, err := f.repo.Move(testCtx(t), it.ID, domain.Placement{})
	if !errors.Is(err, domain.ErrInvalidPlacement) {
		t.Errorf("Move(empty placement) = %v, want ErrInvalidPlacement", err)
	}
	if affected != 0 {
		t.Errorf("Move(empty placement) rows affected = %d, want 0", affected)
	}
}

func TestItemRepository_Move_UnknownBinRejected(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem("Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err := f.repo.Move(testCtx(t), it.ID, domain.PlacementInBin(domain.NewBinID()))
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("Move(unknown bin) = %v, want ErrBinNotFound", err)
	}
}

func TestItemRepository_Move_UnknownHolderRejected(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem("Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err := f.repo.Move(testCtx(t), it.ID, domain.PlacementHeldBy(identity.NewUserID()))
	if !errors.Is(err, identity.ErrUserNotFound) {
		t.Errorf("Move(unknown holder) = %v, want identity.ErrUserNotFound", err)
	}
}

func TestItemRepository_Delete(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem("Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := f.repo.Delete(testCtx(t), it.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	if _, err := f.repo.Get(testCtx(t), viewer, it.ID); !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Get after Delete = %v, want ErrItemNotFound", err)
	}
}

func TestItemRepository_Delete_NotFound(t *testing.T) {
	f := newItemFixture(t)
	err := f.repo.Delete(testCtx(t), domain.NewItemID())
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Delete(unknown) = %v, want ErrItemNotFound", err)
	}
}

// TestBinRepository_Delete_WithItemRejected proves the sprint-level decision
// that item.current_bin_id's ON DELETE RESTRICT makes BinRepository.Delete
// reject deleting a bin that still holds an item — the item-side analog of
// TestLocationRepository_Delete_WithBinRejected in bin_postgres_gated_test.go.
func TestBinRepository_Delete_WithItemRejected(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem("Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create(item): %v", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	err := f.bins.Delete(testCtx(t), viewer, bin)
	if !errors.Is(err, domain.ErrBinNotEmpty) {
		t.Fatalf("Delete(bin with an item) = %v, want ErrBinNotEmpty", err)
	}

	got, err := f.bins.FindVisibleByID(testCtx(t), viewer, bin)
	if err != nil {
		t.Fatalf("FindVisibleByID(bin) after rejected delete: %v", err)
	}
	if got == nil {
		t.Error("Delete(bin with an item) must leave the bin row in place")
	}
}

// TestItemRepository_VisibilityMatrix mirrors bin_postgres_gated_test.go's
// TestBinRepository_PrivateBin_ScopedToCreatorAndAdmin, extended with the
// held-item exception: an item with no current bin (HeldBy set) is always
// visible, regardless of principal, because it has nothing to gate on.
func TestItemRepository_VisibilityMatrix(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	other := f.seedUser(t, identity.RoleMember)
	admin := f.seedUser(t, identity.RoleAdmin)
	loc := f.seedLocation(t, creator)

	publicBin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	privateBin := f.seedBin(t, creator, loc, domain.VisibilityPrivate)

	publicItem := newItem("Public bin item", publicBin, creator)
	privateItem := newItem("Private bin item", privateBin, creator)
	heldItem := &domain.Item{ID: domain.NewItemID(), Name: "Held item", Quantity: 1, HeldBy: &creator, CreatedBy: creator}
	for _, it := range []*domain.Item{publicItem, privateItem, heldItem} {
		if err := f.repo.Create(testCtx(t), it); err != nil {
			t.Fatalf("seed create(%s): %v", it.Name, err)
		}
	}

	principals := []struct {
		name string
		p    identity.Principal
	}{
		{"admin", identity.NewUserPrincipal(admin, identity.RoleAdmin, "Admin")},
		{"creator", identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")},
		{"non-creator member", identity.NewUserPrincipal(other, identity.RoleMember, "Other")},
		{"integration", identity.NewIntegrationPrincipal("Nestova")},
		{"anonymous", identity.Principal{}},
	}

	cases := []struct {
		itemName string
		item     *domain.Item
		visible  func(principalName string) bool
	}{
		{"public bin item", publicItem, func(string) bool { return true }},
		{"private bin item", privateItem, func(n string) bool { return n == "admin" || n == "creator" }},
		{"held item", heldItem, func(string) bool { return true }},
	}

	for _, c := range cases {
		for _, pr := range principals {
			t.Run(c.itemName+"/"+pr.name, func(t *testing.T) {
				_, err := f.repo.Get(testCtx(t), pr.p, c.item.ID)
				assertItemVisibility(t, err, c.visible(pr.name), pr.name, c.itemName)
			})
		}
	}
}

// assertItemVisibility asserts that a Get result matches whether
// principalName should see itemName: nil error when visible,
// domain.ErrItemNotFound when not. Factored out of
// TestItemRepository_VisibilityMatrix's nested case/principal loop so the
// loop body itself stays flat.
func assertItemVisibility(t *testing.T, err error, wantVisible bool, principalName, itemName string) {
	t.Helper()
	if wantVisible && err != nil {
		t.Errorf("Get(%s, %s) = %v, want nil (visible)", principalName, itemName, err)
	}
	if !wantVisible && !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Get(%s, %s) = %v, want ErrItemNotFound (not visible)", principalName, itemName, err)
	}
}

// TestItemRepository_ListByBin_ScopedToVisibility is ListByBin's own version
// of the visibility matrix above, mirroring
// TestBinRepository_PrivateBin_ScopedToCreatorAndAdmin's ListVisible half.
func TestItemRepository_ListByBin_ScopedToVisibility(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	other := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	privateBin := f.seedBin(t, creator, loc, domain.VisibilityPrivate)

	it := newItem("Private bin item", privateBin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	creatorViewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	otherViewer := identity.NewUserPrincipal(other, identity.RoleMember, "Other")

	got, err := f.repo.ListByBin(testCtx(t), otherViewer, privateBin)
	if err != nil {
		t.Fatalf("ListByBin(other): %v", err)
	}
	if len(got) != 0 {
		t.Error("ListByBin(non-creator) must not include an item in another member's private bin")
	}

	got, err = f.repo.ListByBin(testCtx(t), creatorViewer, privateBin)
	if err != nil {
		t.Fatalf("ListByBin(creator): %v", err)
	}
	if len(got) != 1 || got[0].ID != it.ID {
		t.Error("ListByBin(creator) must include the creator's own private-bin item")
	}
}

func TestItemRepository_GetForUpdate(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem("Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := f.repo.GetForUpdate(testCtx(t), it.ID)
	if err != nil {
		t.Fatalf("GetForUpdate: %v", err)
	}
	if got.ID != it.ID || got.Name != it.Name {
		t.Errorf("GetForUpdate = %+v, want it to match the created item", got)
	}
}

func TestItemRepository_GetForUpdate_NotFound(t *testing.T) {
	f := newItemFixture(t)
	_, err := f.repo.GetForUpdate(testCtx(t), domain.NewItemID())
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("GetForUpdate(unknown) = %v, want ErrItemNotFound", err)
	}
}

// TestItemRepository_GetForUpdate_LocksWithinTransaction proves
// GetForUpdate's row lock actually holds across a transaction: a second
// transaction's GetForUpdate on the same row blocks until the first
// transaction ends, which is the entire reason NSTR-29 needs this method
// rather than a plain Get.
func TestItemRepository_GetForUpdate_LocksWithinTransaction(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem("Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	tx1, err := f.pool.Begin(testCtx(t))
	if err != nil {
		t.Fatalf("Begin tx1: %v", err)
	}
	defer func() { _ = tx1.Rollback(context.Background()) }()

	repo1 := adapter.NewItemRepository(tx1)
	if _, err := repo1.GetForUpdate(testCtx(t), it.ID); err != nil {
		t.Fatalf("GetForUpdate(tx1): %v", err)
	}

	tx2, err := f.pool.Begin(testCtx(t))
	if err != nil {
		t.Fatalf("Begin tx2: %v", err)
	}
	defer func() { _ = tx2.Rollback(context.Background()) }()
	repo2 := adapter.NewItemRepository(tx2)

	done := make(chan error, 1)
	go func() {
		lockCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err := repo2.GetForUpdate(lockCtx, it.ID)
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("GetForUpdate(tx2) returned (err=%v) before tx1 released the lock", err)
	case <-time.After(300 * time.Millisecond):
		// Expected: tx2 is blocked waiting on tx1's row lock.
	}

	if err := tx1.Rollback(testCtx(t)); err != nil {
		t.Fatalf("Rollback tx1: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("GetForUpdate(tx2) after tx1 released the lock = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("GetForUpdate(tx2) never completed after tx1 released the lock")
	}
}

func TestNewItemRepository_NilExecutorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewItemRepository(nil) did not panic")
		}
	}()
	adapter.NewItemRepository(nil)
}
