package adapter_test

import (
	"errors"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
)

func TestNewProvisioner_NilPoolPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewProvisioner(nil) did not panic")
		}
	}()
	adapter.NewProvisioner(nil)
}

// TestProvisioner_CreateFirstAdmin_AdoptsSoleExistingHousehold covers
// NSTR-116's household attachment rule's "adopt" branch: when exactly one
// identity.household already exists (the shared-box case, where Nestova
// already created it), the first admin attaches to it rather than a new one.
func TestProvisioner_CreateFirstAdmin_AdoptsSoleExistingHousehold(t *testing.T) {
	pool := dbtest.Harness.NewIsolatedPool(t, "identity")
	households := adapter.NewHouseholdRepository(pool)
	existing := &domain.Household{ID: domain.NewHouseholdID(), Name: "Existing Household"}
	if err := households.Create(testCtx(t), existing); err != nil {
		t.Fatalf("seed existing household: %v", err)
	}

	provisioner := adapter.NewProvisioner(pool)
	u := &domain.User{
		ID:           domain.NewUserID(),
		DisplayName:  "Maya",
		Email:        "maya@example.com",
		PasswordHash: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		Role:         domain.RoleOwner,
		Color:        domain.ColorIndigo,
	}
	if err := provisioner.CreateFirstAdmin(testCtx(t), u); err != nil {
		t.Fatalf("CreateFirstAdmin: %v", err)
	}
	if u.HouseholdID != existing.ID {
		t.Errorf("CreateFirstAdmin attached HouseholdID = %v, want the sole existing household %v", u.HouseholdID, existing.ID)
	}

	all, err := households.List(testCtx(t))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("List after CreateFirstAdmin returned %d households, want still 1 (no new household created)", len(all))
	}
}

// TestProvisioner_CreateFirstAdmin_AmbiguousHouseholdFails covers the "fail
// loudly" branch: several existing households means CreateFirstAdmin must
// not guess which one to attach to.
func TestProvisioner_CreateFirstAdmin_AmbiguousHouseholdFails(t *testing.T) {
	pool := dbtest.Harness.NewIsolatedPool(t, "identity")
	households := adapter.NewHouseholdRepository(pool)
	if err := households.Create(testCtx(t), &domain.Household{ID: domain.NewHouseholdID(), Name: "One"}); err != nil {
		t.Fatalf("seed household one: %v", err)
	}
	if err := households.Create(testCtx(t), &domain.Household{ID: domain.NewHouseholdID(), Name: "Two"}); err != nil {
		t.Fatalf("seed household two: %v", err)
	}

	provisioner := adapter.NewProvisioner(pool)
	u := &domain.User{
		ID:           domain.NewUserID(),
		DisplayName:  "Maya",
		Email:        "maya@example.com",
		PasswordHash: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		Role:         domain.RoleOwner,
		Color:        domain.ColorIndigo,
	}
	err := provisioner.CreateFirstAdmin(testCtx(t), u)
	if !errors.Is(err, domain.ErrAmbiguousHousehold) {
		t.Errorf("CreateFirstAdmin(ambiguous households) error = %v, want ErrAmbiguousHousehold", err)
	}

	repo := adapter.NewUserRepository(pool)
	has, hasErr := repo.HasAnyUser(testCtx(t))
	if hasErr != nil {
		t.Fatalf("HasAnyUser: %v", hasErr)
	}
	if has {
		t.Error("CreateFirstAdmin must not persist a user when household resolution fails")
	}
}
