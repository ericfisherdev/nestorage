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

// locationFixture wires a LocationRepository and an identity UserRepository
// over ONE derived database (dbtest.Harness.NewIsolatedPool must be called
// exactly once per test — a second call resets the schema it just built,
// wiping any data already written), so a test can seed the app_user row
// location's created_by FK requires and then exercise the repository under
// test.
type locationFixture struct {
	pool  *pgxpool.Pool
	repo  *adapter.LocationRepository
	users *identityadapter.UserRepository
}

// newLocationFixture derives this package's own "storage" database — the
// one suffix every gated test in this package uses, mirroring identity's
// "identity" suffix.
func newLocationFixture(t *testing.T) *locationFixture {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "storage")
	return &locationFixture{
		pool:  pool,
		repo:  adapter.NewLocationRepository(pool),
		users: identityadapter.NewUserRepository(pool),
	}
}

// testCtx returns a per-call context bounded so a slow/unresponsive database
// fails the test rather than hanging it.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// seedOwner creates and returns the id of a user for location's created_by
// FK — the same shape as identity's deviceTokenFixture.seedOwner.
func (f *locationFixture) seedOwner(t *testing.T) identity.UserID {
	t.Helper()
	u := &identity.User{
		ID:           identity.NewUserID(),
		DisplayName:  "Maya",
		Email:        "location-owner-" + identity.NewUserID().String() + "@example.com",
		PasswordHash: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		Role:         identity.RoleMember,
		Color:        identity.ColorIndigo,
	}
	if err := f.users.Create(testCtx(t), u); err != nil {
		t.Fatalf("seed location owner: %v", err)
	}
	return u.ID
}

func newLocation(createdBy identity.UserID, name string) *domain.Location {
	return &domain.Location{
		ID:        domain.NewLocationID(),
		Name:      name,
		CreatedBy: createdBy,
	}
}

func TestLocationRepository_CreateAndFindByID(t *testing.T) {
	f := newLocationFixture(t)
	owner := f.seedOwner(t)
	loc := newLocation(owner, "Garage")
	loc.Description = "Attached two-car garage"

	if err := f.repo.Create(testCtx(t), loc); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if loc.CreatedAt.IsZero() || loc.UpdatedAt.IsZero() {
		t.Error("Create left CreatedAt/UpdatedAt zero")
	}

	got, err := f.repo.FindByID(testCtx(t), loc.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.ID != loc.ID || got.Name != "Garage" || got.Description != loc.Description || got.CreatedBy != owner {
		t.Errorf("FindByID = %+v, want it to match the created location", got)
	}
	if got.ParentID != nil {
		t.Errorf("FindByID.ParentID = %v, want nil for a top-level location", got.ParentID)
	}
}

func TestLocationRepository_CreateWithParent(t *testing.T) {
	f := newLocationFixture(t)
	owner := f.seedOwner(t)
	parent := newLocation(owner, "Garage")
	if err := f.repo.Create(testCtx(t), parent); err != nil {
		t.Fatalf("Create(parent): %v", err)
	}

	child := newLocation(owner, "Garage / Shelf B")
	child.ParentID = &parent.ID
	if err := f.repo.Create(testCtx(t), child); err != nil {
		t.Fatalf("Create(child): %v", err)
	}

	got, err := f.repo.FindByID(testCtx(t), child.ID)
	if err != nil {
		t.Fatalf("FindByID(child): %v", err)
	}
	if got.ParentID == nil || *got.ParentID != parent.ID {
		t.Errorf("FindByID(child).ParentID = %v, want %v", got.ParentID, parent.ID)
	}
}

func TestLocationRepository_FindByID_NotFound(t *testing.T) {
	f := newLocationFixture(t)
	_, err := f.repo.FindByID(testCtx(t), domain.NewLocationID())
	if !errors.Is(err, domain.ErrLocationNotFound) {
		t.Errorf("FindByID(unknown) = %v, want ErrLocationNotFound", err)
	}
}

func TestLocationRepository_List_Ordered(t *testing.T) {
	f := newLocationFixture(t)
	owner := f.seedOwner(t)
	if err := f.repo.Create(testCtx(t), newLocation(owner, "Hall closet")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.repo.Create(testCtx(t), newLocation(owner, "Garage")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := f.repo.List(testCtx(t))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List returned %d locations, want 2", len(got))
	}
	if got[0].Name != "Garage" || got[1].Name != "Hall closet" {
		t.Errorf("List = [%q, %q], want alphabetical [Garage, Hall closet]", got[0].Name, got[1].Name)
	}
}

func TestLocationRepository_List_Empty(t *testing.T) {
	f := newLocationFixture(t)
	got, err := f.repo.List(testCtx(t))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List on an empty database = %d locations, want 0", len(got))
	}
}

func TestLocationRepository_Rename(t *testing.T) {
	f := newLocationFixture(t)
	owner := f.seedOwner(t)
	loc := newLocation(owner, "Garage")
	if err := f.repo.Create(testCtx(t), loc); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := f.repo.Rename(testCtx(t), loc.ID, "Attached garage"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	got, err := f.repo.FindByID(testCtx(t), loc.ID)
	if err != nil {
		t.Fatalf("FindByID after Rename: %v", err)
	}
	if got.Name != "Attached garage" {
		t.Errorf("Name after Rename = %q, want %q", got.Name, "Attached garage")
	}
}

func TestLocationRepository_Rename_NotFound(t *testing.T) {
	f := newLocationFixture(t)
	err := f.repo.Rename(testCtx(t), domain.NewLocationID(), "Ghost")
	if !errors.Is(err, domain.ErrLocationNotFound) {
		t.Errorf("Rename(unknown) = %v, want ErrLocationNotFound", err)
	}
}

func TestLocationRepository_Delete(t *testing.T) {
	f := newLocationFixture(t)
	owner := f.seedOwner(t)
	loc := newLocation(owner, "Garage")
	if err := f.repo.Create(testCtx(t), loc); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := f.repo.Delete(testCtx(t), loc.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := f.repo.FindByID(testCtx(t), loc.ID)
	if !errors.Is(err, domain.ErrLocationNotFound) {
		t.Errorf("FindByID after Delete = %v, want ErrLocationNotFound", err)
	}
}

func TestLocationRepository_Delete_NotFound(t *testing.T) {
	f := newLocationFixture(t)
	err := f.repo.Delete(testCtx(t), domain.NewLocationID())
	if !errors.Is(err, domain.ErrLocationNotFound) {
		t.Errorf("Delete(unknown) = %v, want ErrLocationNotFound", err)
	}
}

// TestLocationRepository_Delete_WithChildRejected exercises the exact FK
// guard bins will trip once NSTR-27 lands: deleting a location that still
// has a dependent row fails with ErrLocationNotEmpty and changes nothing.
func TestLocationRepository_Delete_WithChildRejected(t *testing.T) {
	f := newLocationFixture(t)
	owner := f.seedOwner(t)
	parent := newLocation(owner, "Garage")
	if err := f.repo.Create(testCtx(t), parent); err != nil {
		t.Fatalf("Create(parent): %v", err)
	}
	child := newLocation(owner, "Garage / Shelf B")
	child.ParentID = &parent.ID
	if err := f.repo.Create(testCtx(t), child); err != nil {
		t.Fatalf("Create(child): %v", err)
	}

	err := f.repo.Delete(testCtx(t), parent.ID)
	if !errors.Is(err, domain.ErrLocationNotEmpty) {
		t.Fatalf("Delete(parent with a child) = %v, want ErrLocationNotEmpty", err)
	}

	got, err := f.repo.FindByID(testCtx(t), parent.ID)
	if err != nil {
		t.Fatalf("FindByID(parent) after rejected delete: %v", err)
	}
	if got == nil {
		t.Error("Delete(parent with a child) must leave the parent row in place")
	}
}

func TestNewLocationRepository_NilExecutorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewLocationRepository(nil) did not panic")
		}
	}()
	adapter.NewLocationRepository(nil)
}
