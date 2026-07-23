package adapter_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
	"github.com/ericfisherdev/nestorage/internal/storage/adapter"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// binFixture wires a BinRepository alongside the LocationRepository and
// UserRepository bin's foreign keys require, over ONE derived database —
// dbtest.Harness.NewIsolatedPool must be called exactly once per test, and
// this shares locationFixture's "storage" suffix (the suffix is per
// *package*, not per aggregate: two fixtures in this package using
// different suffixes would defeat the isolation the harness provides).
type binFixture struct {
	pool      *pgxpool.Pool
	repo      *adapter.BinRepository
	locations *adapter.LocationRepository
	users     *identityadapter.UserRepository
}

func newBinFixture(t *testing.T) *binFixture {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "storage")
	return &binFixture{
		pool:      pool,
		repo:      adapter.NewBinRepository(pool),
		locations: adapter.NewLocationRepository(pool),
		users:     identityadapter.NewUserRepository(pool),
	}
}

// seedUser creates and returns the id of a user with the given role, for
// bin's owner_id/created_by FKs and for building viewer principals.
func (f *binFixture) seedUser(t *testing.T, role identity.Role) identity.UserID {
	t.Helper()
	u := &identity.User{
		ID:           identity.NewUserID(),
		DisplayName:  "Test User",
		Email:        "bin-user-" + identity.NewUserID().String() + "@example.com",
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
func (f *binFixture) seedLocation(t *testing.T, createdBy identity.UserID) domain.LocationID {
	t.Helper()
	loc := &domain.Location{ID: domain.NewLocationID(), Name: "Garage", CreatedBy: createdBy}
	if err := f.locations.Create(testCtx(t), loc); err != nil {
		t.Fatalf("seed location: %v", err)
	}
	return loc.ID
}

func newBin(code string, location domain.LocationID, createdBy identity.UserID) *domain.Bin {
	return &domain.Bin{
		ID:         domain.NewBinID(),
		Code:       code,
		Name:       "Bin " + code,
		LocationID: location,
		CreatedBy:  createdBy,
	}
}

func TestBinRepository_CreateAndFindVisibleByID(t *testing.T) {
	f := newBinFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := newBin("A1", loc, creator)
	bin.Description = "Camping gear"

	if err := f.repo.Create(testCtx(t), bin); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if bin.CreatedAt.IsZero() || bin.UpdatedAt.IsZero() {
		t.Error("Create left CreatedAt/UpdatedAt zero")
	}
	if bin.Visibility != domain.VisibilityPublic {
		t.Errorf("Create defaulted Visibility = %q, want %q", bin.Visibility, domain.VisibilityPublic)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.repo.FindVisibleByID(testCtx(t), viewer, bin.ID)
	if err != nil {
		t.Fatalf("FindVisibleByID: %v", err)
	}
	if got.Code != "A1" || got.Name != bin.Name || got.Description != bin.Description ||
		got.LocationID != loc || got.CreatedBy != creator {
		t.Errorf("FindVisibleByID = %+v, want it to match the created bin", got)
	}
	if got.OwnerID != nil {
		t.Errorf("FindVisibleByID.OwnerID = %v, want nil for an unowned bin", got.OwnerID)
	}
}

func TestBinRepository_FindVisibleByCode_Normalizes(t *testing.T) {
	f := newBinFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := newBin("A2", loc, creator)
	if err := f.repo.Create(testCtx(t), bin); err != nil {
		t.Fatalf("Create: %v", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.repo.FindVisibleByCode(testCtx(t), viewer, "  a2 ")
	if err != nil {
		t.Fatalf("FindVisibleByCode: %v", err)
	}
	if got.ID != bin.ID {
		t.Errorf("FindVisibleByCode(%q) = %v, want %v", "  a2 ", got.ID, bin.ID)
	}
}

func TestBinRepository_FindVisibleByCode_NotFound(t *testing.T) {
	f := newBinFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")

	_, err := f.repo.FindVisibleByCode(testCtx(t), viewer, "GHOST")
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("FindVisibleByCode(unknown) = %v, want ErrBinNotFound", err)
	}
}

func TestBinRepository_Create_DuplicateCodeRejected(t *testing.T) {
	f := newBinFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	if err := f.repo.Create(testCtx(t), newBin("DUP1", loc, creator)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err := f.repo.Create(testCtx(t), newBin("DUP1", loc, creator))
	if !errors.Is(err, domain.ErrDuplicateBinCode) {
		t.Errorf("Create(duplicate code) = %v, want ErrDuplicateBinCode", err)
	}
}

func TestBinRepository_Create_UnknownLocationRejected(t *testing.T) {
	f := newBinFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	bin := newBin("A3", domain.NewLocationID(), creator)

	err := f.repo.Create(testCtx(t), bin)
	if !errors.Is(err, domain.ErrLocationNotFound) {
		t.Errorf("Create(unknown location) = %v, want ErrLocationNotFound", err)
	}
}

func TestBinRepository_Create_UnknownCreatedByRejected(t *testing.T) {
	f := newBinFixture(t)
	admin := f.seedUser(t, identity.RoleAdmin)
	loc := f.seedLocation(t, admin)
	bin := newBin("A4", loc, identity.NewUserID())

	err := f.repo.Create(testCtx(t), bin)
	if !errors.Is(err, identity.ErrUserNotFound) {
		t.Errorf("Create(unknown created_by) = %v, want identity.ErrUserNotFound", err)
	}
}

func TestBinRepository_Create_UnknownOwnerRejected(t *testing.T) {
	f := newBinFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := newBin("A5", loc, creator)
	unknownOwner := identity.NewUserID()
	bin.OwnerID = &unknownOwner

	err := f.repo.Create(testCtx(t), bin)
	if !errors.Is(err, identity.ErrUserNotFound) {
		t.Errorf("Create(unknown owner) = %v, want identity.ErrUserNotFound", err)
	}
}

func TestBinRepository_Create_OwnerRoundTrips(t *testing.T) {
	f := newBinFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	owner := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)

	shared := newBin("SH1", loc, creator)
	owned := newBin("OW1", loc, creator)
	owned.OwnerID = &owner

	if err := f.repo.Create(testCtx(t), shared); err != nil {
		t.Fatalf("Create(shared): %v", err)
	}
	if err := f.repo.Create(testCtx(t), owned); err != nil {
		t.Fatalf("Create(owned): %v", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")

	gotShared, err := f.repo.FindVisibleByID(testCtx(t), viewer, shared.ID)
	if err != nil {
		t.Fatalf("FindVisibleByID(shared): %v", err)
	}
	if gotShared.OwnerID != nil {
		t.Errorf("shared bin OwnerID = %v, want nil", gotShared.OwnerID)
	}

	gotOwned, err := f.repo.FindVisibleByID(testCtx(t), viewer, owned.ID)
	if err != nil {
		t.Fatalf("FindVisibleByID(owned): %v", err)
	}
	if gotOwned.OwnerID == nil || *gotOwned.OwnerID != owner {
		t.Errorf("owned bin OwnerID = %v, want %v", gotOwned.OwnerID, owner)
	}
}

// TestBinRepository_PrivateBin_ScopedToCreatorAndAdmin is the headline case:
// a member cannot read another member's private bin, by id or in
// ListVisible, while its creator and an admin can.
func TestBinRepository_PrivateBin_ScopedToCreatorAndAdmin(t *testing.T) {
	f := newBinFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	other := f.seedUser(t, identity.RoleMember)
	admin := f.seedUser(t, identity.RoleAdmin)
	loc := f.seedLocation(t, creator)

	private := newBin("PRIV1", loc, creator)
	private.Visibility = domain.VisibilityPrivate
	if err := f.repo.Create(testCtx(t), private); err != nil {
		t.Fatalf("Create: %v", err)
	}

	creatorViewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	otherViewer := identity.NewUserPrincipal(other, identity.RoleMember, "Other")
	adminViewer := identity.NewUserPrincipal(admin, identity.RoleAdmin, "Admin")

	if _, err := f.repo.FindVisibleByID(testCtx(t), creatorViewer, private.ID); err != nil {
		t.Errorf("creator FindVisibleByID = %v, want nil", err)
	}
	if _, err := f.repo.FindVisibleByID(testCtx(t), adminViewer, private.ID); err != nil {
		t.Errorf("admin FindVisibleByID = %v, want nil", err)
	}
	if _, err := f.repo.FindVisibleByID(testCtx(t), otherViewer, private.ID); !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("non-creator FindVisibleByID = %v, want ErrBinNotFound", err)
	}

	otherList, err := f.repo.ListVisible(testCtx(t), otherViewer)
	if err != nil {
		t.Fatalf("ListVisible(other): %v", err)
	}
	for _, b := range otherList {
		if b.ID == private.ID {
			t.Error("ListVisible(non-creator) must not include another member's private bin")
		}
	}

	creatorList, err := f.repo.ListVisible(testCtx(t), creatorViewer)
	if err != nil {
		t.Fatalf("ListVisible(creator): %v", err)
	}
	found := false
	for _, b := range creatorList {
		if b.ID == private.ID {
			found = true
		}
	}
	if !found {
		t.Error("ListVisible(creator) must include the creator's own private bin")
	}
}

func TestBinRepository_ListVisible_Empty(t *testing.T) {
	f := newBinFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")

	got, err := f.repo.ListVisible(testCtx(t), viewer)
	if err != nil {
		t.Fatalf("ListVisible: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListVisible on an empty database = %d bins, want 0", len(got))
	}
}

// TestBinRepository_UpdateVisibility exercises the mutate predicate on a
// private bin, where it actually restricts anything: CanMutateBin (which
// UpdateVisibility's WHERE mirrors) returns true unconditionally for a
// public bin — see TestBinRepository_UpdateVisibility_PublicBinMutableByAnyone
// — so a public fixture here would prove nothing about scoping.
func TestBinRepository_UpdateVisibility(t *testing.T) {
	f := newBinFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	other := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := newBin("UPD1", loc, creator)
	bin.Visibility = domain.VisibilityPrivate
	if err := f.repo.Create(testCtx(t), bin); err != nil {
		t.Fatalf("Create: %v", err)
	}

	creatorViewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	otherViewer := identity.NewUserPrincipal(other, identity.RoleMember, "Other")

	if err := f.repo.UpdateVisibility(testCtx(t), otherViewer, bin.ID, domain.VisibilityPublic); !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("UpdateVisibility(non-creator, private bin) = %v, want ErrBinNotFound", err)
	}

	if err := f.repo.UpdateVisibility(testCtx(t), creatorViewer, bin.ID, domain.VisibilityPublic); err != nil {
		t.Fatalf("UpdateVisibility(creator): %v", err)
	}

	got, err := f.repo.FindVisibleByID(testCtx(t), creatorViewer, bin.ID)
	if err != nil {
		t.Fatalf("FindVisibleByID after update: %v", err)
	}
	if got.Visibility != domain.VisibilityPublic {
		t.Errorf("Visibility after UpdateVisibility = %q, want %q", got.Visibility, domain.VisibilityPublic)
	}
}

// TestBinRepository_UpdateVisibility_PublicBinMutableByAnyone documents a
// consequence of CanMutateBin's own doc (identity/domain/authz.go): today it
// is the exact same rule as CanSeeBin, so a public bin — readable by
// anyone — is also mutable by anyone, not only its creator or an admin.
// This is deliberate, not a gap: CanMutateBin is kept as its own function
// specifically so a later ticket can tighten mutation without touching read
// visibility, and this test is what would catch that tightening not being
// mirrored here.
func TestBinRepository_UpdateVisibility_PublicBinMutableByAnyone(t *testing.T) {
	f := newBinFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	other := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := newBin("PUB1", loc, creator)
	if err := f.repo.Create(testCtx(t), bin); err != nil {
		t.Fatalf("Create: %v", err)
	}

	otherViewer := identity.NewUserPrincipal(other, identity.RoleMember, "Other")
	if err := f.repo.UpdateVisibility(testCtx(t), otherViewer, bin.ID, domain.VisibilityPrivate); err != nil {
		t.Errorf("UpdateVisibility(non-creator, public bin) = %v, want nil under today's CanMutateBin", err)
	}
}

func TestBinRepository_UpdateVisibility_NotFound(t *testing.T) {
	f := newBinFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")

	err := f.repo.UpdateVisibility(testCtx(t), viewer, domain.NewBinID(), domain.VisibilityPrivate)
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("UpdateVisibility(unknown) = %v, want ErrBinNotFound", err)
	}
}

// TestBinRepository_Delete exercises the mutate predicate on a private bin —
// see TestBinRepository_UpdateVisibility's doc for why a public fixture
// would not exercise scoping at all.
func TestBinRepository_Delete(t *testing.T) {
	f := newBinFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	other := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := newBin("DEL1", loc, creator)
	bin.Visibility = domain.VisibilityPrivate
	if err := f.repo.Create(testCtx(t), bin); err != nil {
		t.Fatalf("Create: %v", err)
	}

	creatorViewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	otherViewer := identity.NewUserPrincipal(other, identity.RoleMember, "Other")

	if err := f.repo.Delete(testCtx(t), otherViewer, bin.ID); !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("Delete(non-creator, private bin) = %v, want ErrBinNotFound", err)
	}

	if err := f.repo.Delete(testCtx(t), creatorViewer, bin.ID); err != nil {
		t.Fatalf("Delete(creator): %v", err)
	}

	if _, err := f.repo.FindVisibleByID(testCtx(t), creatorViewer, bin.ID); !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("FindVisibleByID after Delete = %v, want ErrBinNotFound", err)
	}
}

func TestBinRepository_Delete_NotFound(t *testing.T) {
	f := newBinFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")

	err := f.repo.Delete(testCtx(t), viewer, domain.NewBinID())
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("Delete(unknown) = %v, want ErrBinNotFound", err)
	}
}

// TestLocationRepository_Delete_WithBinRejected proves the sprint-level
// decision that bin.location_id's ON DELETE RESTRICT makes
// LocationRepository.Delete's existing ErrLocationNotEmpty guard
// (00006_locations.sql, exercised for a child location by
// TestLocationRepository_Delete_WithChildRejected in postgres_test.go)
// cover bins too, not only child locations.
func TestLocationRepository_Delete_WithBinRejected(t *testing.T) {
	f := newBinFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := newBin("LOC1", loc, creator)
	if err := f.repo.Create(testCtx(t), bin); err != nil {
		t.Fatalf("Create(bin): %v", err)
	}

	err := f.locations.Delete(testCtx(t), loc)
	if !errors.Is(err, domain.ErrLocationNotEmpty) {
		t.Fatalf("Delete(location with a bin) = %v, want ErrLocationNotEmpty", err)
	}

	got, err := f.locations.FindByID(testCtx(t), loc)
	if err != nil {
		t.Fatalf("FindByID(location) after rejected delete: %v", err)
	}
	if got == nil {
		t.Error("Delete(location with a bin) must leave the location row in place")
	}
}

func TestNewBinRepository_NilExecutorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewBinRepository(nil) did not panic")
		}
	}()
	adapter.NewBinRepository(nil)
}
