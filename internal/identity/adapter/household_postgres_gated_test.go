package adapter_test

import (
	"testing"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
)

// newTestHouseholdRepo returns a HouseholdRepository over this package's own
// "identity" derived database — the same suffix newTestRepo uses.
func newTestHouseholdRepo(t *testing.T) *adapter.HouseholdRepository {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "identity")
	return adapter.NewHouseholdRepository(pool)
}

func TestNewHouseholdRepository_NilExecutorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewHouseholdRepository(nil) did not panic")
		}
	}()
	adapter.NewHouseholdRepository(nil)
}

func TestHouseholdRepository_Create_NilHouseholdErrors(t *testing.T) {
	repo := newTestHouseholdRepo(t)
	if err := repo.Create(testCtx(t), nil); err == nil {
		t.Error("Create(nil) = nil error, want an error")
	}
}

func TestHouseholdRepository_ListEmpty(t *testing.T) {
	repo := newTestHouseholdRepo(t)
	got, err := repo.List(testCtx(t))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List on an empty database = %d households, want 0", len(got))
	}
}

func TestHouseholdRepository_CreateAndList(t *testing.T) {
	repo := newTestHouseholdRepo(t)
	h := &domain.Household{ID: domain.NewHouseholdID(), Name: "The Fishers"}
	if err := repo.Create(testCtx(t), h); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if h.CreatedAt.IsZero() || h.UpdatedAt.IsZero() {
		t.Error("Create left CreatedAt/UpdatedAt zero")
	}

	got, err := repo.List(testCtx(t))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List returned %d households, want 1", len(got))
	}
	if got[0].ID != h.ID || got[0].Name != h.Name {
		t.Errorf("List = %+v, want a match for %+v", got[0], h)
	}
}

func TestHouseholdRepository_ListOrdersByCreatedAt(t *testing.T) {
	repo := newTestHouseholdRepo(t)
	first := &domain.Household{ID: domain.NewHouseholdID(), Name: "First"}
	if err := repo.Create(testCtx(t), first); err != nil {
		t.Fatalf("Create(first): %v", err)
	}
	second := &domain.Household{ID: domain.NewHouseholdID(), Name: "Second"}
	if err := repo.Create(testCtx(t), second); err != nil {
		t.Fatalf("Create(second): %v", err)
	}

	got, err := repo.List(testCtx(t))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List returned %d households, want 2", len(got))
	}
	if got[0].ID != first.ID || got[1].ID != second.ID {
		t.Errorf("List order = [%v, %v], want [first, second] by created_at", got[0].ID, got[1].ID)
	}
}
