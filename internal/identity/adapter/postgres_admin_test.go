package adapter_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// TestSetActive_LastActiveAdminRejected is the automated equivalent of this
// ticket's "the last active admin cannot be deactivated" acceptance
// criterion.
func TestSetActive_LastActiveAdminRejected(t *testing.T) {
	repo := newTestRepo(t)
	admin := seedAdmin(t, repo, "admin@example.com")

	err := repo.SetActive(testCtx(t), admin.ID, false)
	if !errors.Is(err, domain.ErrLastActiveAdmin) {
		t.Fatalf("SetActive(false) on the only active admin = %v, want ErrLastActiveAdmin", err)
	}

	got, findErr := repo.FindByID(testCtx(t), admin.ID)
	if findErr != nil {
		t.Fatalf("FindByID: %v", findErr)
	}
	if !got.Active {
		t.Error("a rejected deactivation must leave the row untouched — Active is now false")
	}
}

// TestSetRole_LastActiveAdminRejected is the automated equivalent of this
// ticket's "the last active admin cannot be demoted" acceptance criterion.
func TestSetRole_LastActiveAdminRejected(t *testing.T) {
	repo := newTestRepo(t)
	admin := seedAdmin(t, repo, "admin@example.com")

	err := repo.SetRole(testCtx(t), admin.ID, domain.RoleMember)
	if !errors.Is(err, domain.ErrLastActiveAdmin) {
		t.Fatalf("SetRole(member) on the only active admin = %v, want ErrLastActiveAdmin", err)
	}

	got, findErr := repo.FindByID(testCtx(t), admin.ID)
	if findErr != nil {
		t.Fatalf("FindByID: %v", findErr)
	}
	if got.Role != domain.RoleAdmin {
		t.Error("a rejected demotion must leave the row untouched — Role is now member")
	}
}

// TestSetActive_DeactivatedUserFieldsIntact is the automated equivalent of
// "history entries created by a deactivated user still render with their
// name": FindByID must keep returning the full row — including display
// name — after deactivation, never an error or a blanked field. A second
// admin is seeded first so the deactivation itself is not rejected.
func TestSetActive_DeactivatedUserFieldsIntact(t *testing.T) {
	repo := newTestRepo(t)
	seedAdmin(t, repo, "admin@example.com")
	member := seedUser(t, repo, "maya@example.com")

	if err := repo.SetActive(testCtx(t), member.ID, false); err != nil {
		t.Fatalf("SetActive(false): %v", err)
	}

	got, err := repo.FindByID(testCtx(t), member.ID)
	if err != nil {
		t.Fatalf("FindByID after deactivate: %v", err)
	}
	if got.Active {
		t.Error("Active = true after SetActive(false)")
	}
	if got.DisplayName != member.DisplayName {
		t.Errorf("DisplayName after deactivate = %q, want %q (still resolvable)", got.DisplayName, member.DisplayName)
	}
}

// TestSetRole_ConcurrentDemotionsOfTwoAdmins_LeaveExactlyOneActiveAdmin is
// the test the row-lock transaction in lastAdminGuardedUpdate exists for:
// two admins, demoted concurrently, must leave exactly one active admin —
// never zero (a plain read-then-write would let both succeed) and never
// both rejected (the lock must not deadlock or over-reject).
func TestSetRole_ConcurrentDemotionsOfTwoAdmins_LeaveExactlyOneActiveAdmin(t *testing.T) {
	repo := newTestRepo(t)
	adminA := seedAdmin(t, repo, "admin-a@example.com")
	adminB := seedAdmin(t, repo, "admin-b@example.com")
	ctx := testCtx(t)

	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = repo.SetRole(ctx, adminA.ID, domain.RoleMember)
	}()
	go func() {
		defer wg.Done()
		errs[1] = repo.SetRole(ctx, adminB.ID, domain.RoleMember)
	}()
	wg.Wait()

	succeeded, rejected := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, domain.ErrLastActiveAdmin):
			rejected++
		default:
			t.Fatalf("SetRole returned an unexpected error: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("succeeded=%d rejected=%d, want exactly one of each", succeeded, rejected)
	}

	activeAdmins := 0
	for _, id := range []domain.UserID{adminA.ID, adminB.ID} {
		u, err := repo.FindByID(ctx, id)
		if err != nil {
			t.Fatalf("FindByID: %v", err)
		}
		if u.Role == domain.RoleAdmin && u.Active {
			activeAdmins++
		}
	}
	if activeAdmins != 1 {
		t.Errorf("active admins after two concurrent demotions = %d, want exactly 1", activeAdmins)
	}
}
