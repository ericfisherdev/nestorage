package adapter_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
)

// newTestRepo returns a repository over this package's own derived database,
// freshly reset and migrated. dbtest.Harness.NewIsolatedPool owns the safety
// rail, the on-demand CREATE DATABASE, and the reset/migrate lifecycle. The
// "identity" suffix must stay unique across the repo's gated test packages.
func newTestRepo(t *testing.T) *adapter.UserRepository {
	t.Helper()
	return adapter.NewUserRepository(dbtest.Harness.NewIsolatedPool(t, "identity"))
}

// testCtx returns a per-call context bounded so a slow/unresponsive database
// fails the test rather than hanging it.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func newUser(email string) *domain.User {
	return &domain.User{
		ID:           domain.NewUserID(),
		DisplayName:  "Maya",
		Email:        email,
		PasswordHash: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		Role:         domain.RoleMember,
		Color:        domain.ColorIndigo,
	}
}

func seedUser(t *testing.T, repo *adapter.UserRepository, email string) *domain.User {
	t.Helper()
	u := newUser(email)
	if err := repo.Create(testCtx(t), u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return u
}

// seedAdmin is seedUser's admin-role twin, used by tests that need an
// active admin already present so a later mutation on a DIFFERENT user
// never trips the last-active-admin guard (see postgres_admin_test.go for
// the tests that deliberately DO trip it).
func seedAdmin(t *testing.T, repo *adapter.UserRepository, email string) *domain.User {
	t.Helper()
	u := newUser(email)
	u.Role = domain.RoleAdmin
	if err := repo.Create(testCtx(t), u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return u
}

func TestCreateAndFindByID(t *testing.T) {
	repo := newTestRepo(t)
	u := seedUser(t, repo, "maya@example.com")

	if !u.Active {
		t.Error("Create left Active = false, want true (the app_user.active column defaults true)")
	}
	if u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
		t.Error("Create left CreatedAt/UpdatedAt zero")
	}

	got, err := repo.FindByID(testCtx(t), u.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.ID != u.ID || got.Email != u.Email || got.Role != u.Role || got.Color != u.Color {
		t.Errorf("FindByID = %+v, want a match for %+v", got, u)
	}
}

func TestFindByIDNotFound(t *testing.T) {
	repo := newTestRepo(t)
	_, err := repo.FindByID(testCtx(t), domain.NewUserID())
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("FindByID(unknown) = %v, want ErrUserNotFound", err)
	}
}

func TestFindByEmailIsCaseInsensitive(t *testing.T) {
	repo := newTestRepo(t)
	u := seedUser(t, repo, "maya@example.com")

	got, err := repo.FindByEmail(testCtx(t), "MAYA@EXAMPLE.COM")
	if err != nil {
		t.Fatalf("FindByEmail (differently cased): %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("FindByEmail(differently cased) = id %v, want %v", got.ID, u.ID)
	}
}

func TestFindByEmailNotFound(t *testing.T) {
	repo := newTestRepo(t)
	_, err := repo.FindByEmail(testCtx(t), "nobody@example.com")
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("FindByEmail(unknown) = %v, want ErrUserNotFound", err)
	}
}

func TestCreateDuplicateEmailRejectedCaseInsensitively(t *testing.T) {
	repo := newTestRepo(t)
	seedUser(t, repo, "maya@example.com")

	dup := newUser("MAYA@EXAMPLE.COM")
	err := repo.Create(testCtx(t), dup)
	if !errors.Is(err, domain.ErrDuplicateEmail) {
		t.Errorf("Create(email differing only in case) = %v, want ErrDuplicateEmail", err)
	}
}

func TestList(t *testing.T) {
	repo := newTestRepo(t)
	seedUser(t, repo, "ivy@example.com")
	seedUser(t, repo, "daniel@example.com")

	users, err := repo.List(testCtx(t))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("List returned %d users, want 2", len(users))
	}
}

func TestListEmpty(t *testing.T) {
	repo := newTestRepo(t)
	users, err := repo.List(testCtx(t))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("List on an empty database = %d users, want 0", len(users))
	}
}

func TestUpdate(t *testing.T) {
	repo := newTestRepo(t)
	u := seedUser(t, repo, "maya@example.com")

	u.DisplayName = "Maya Fisher"
	u.Email = "maya.fisher@example.com"
	u.Role = domain.RoleAdmin
	u.Color = domain.ColorTeal
	if err := repo.Update(testCtx(t), u); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := repo.FindByID(testCtx(t), u.ID)
	if err != nil {
		t.Fatalf("FindByID after Update: %v", err)
	}
	if got.DisplayName != "Maya Fisher" || got.Email != "maya.fisher@example.com" ||
		got.Role != domain.RoleAdmin || got.Color != domain.ColorTeal {
		t.Errorf("FindByID after Update = %+v, want the updated fields", got)
	}
}

func TestUpdateNotFound(t *testing.T) {
	repo := newTestRepo(t)
	u := newUser("ghost@example.com")
	u.ID = domain.NewUserID()

	err := repo.Update(testCtx(t), u)
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("Update(unknown id) = %v, want ErrUserNotFound", err)
	}
}

func TestUpdateDuplicateEmail(t *testing.T) {
	repo := newTestRepo(t)
	seedUser(t, repo, "ivy@example.com")
	daniel := seedUser(t, repo, "daniel@example.com")

	daniel.Email = "IVY@EXAMPLE.COM"
	err := repo.Update(testCtx(t), daniel)
	if !errors.Is(err, domain.ErrDuplicateEmail) {
		t.Errorf("Update(email differing only in case from another user) = %v, want ErrDuplicateEmail", err)
	}
}

func TestSetActiveBothDirections(t *testing.T) {
	repo := newTestRepo(t)
	u := seedUser(t, repo, "maya@example.com")

	if err := repo.SetActive(testCtx(t), u.ID, false); err != nil {
		t.Fatalf("SetActive(false): %v", err)
	}
	got, err := repo.FindByID(testCtx(t), u.ID)
	if err != nil {
		t.Fatalf("FindByID after deactivate: %v", err)
	}
	if got.Active {
		t.Error("FindByID after SetActive(false) = Active true, want false")
	}

	if err := repo.SetActive(testCtx(t), u.ID, true); err != nil {
		t.Fatalf("SetActive(true): %v", err)
	}
	got, err = repo.FindByID(testCtx(t), u.ID)
	if err != nil {
		t.Fatalf("FindByID after reactivate: %v", err)
	}
	if !got.Active {
		t.Error("FindByID after SetActive(true) = Active false, want true")
	}
}

func TestSetActiveNotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.SetActive(testCtx(t), domain.NewUserID(), false)
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("SetActive(unknown id) = %v, want ErrUserNotFound", err)
	}
}

// TestSetRolePromotesAndDemotes seeds a second admin first so demoting the
// member-under-test back to member never trips the last-active-admin guard
// — that guard's rejection path is covered separately in
// postgres_admin_test.go.
func TestSetRolePromotesAndDemotes(t *testing.T) {
	repo := newTestRepo(t)
	seedAdmin(t, repo, "admin@example.com")
	member := seedUser(t, repo, "maya@example.com")

	if err := repo.SetRole(testCtx(t), member.ID, domain.RoleAdmin); err != nil {
		t.Fatalf("SetRole(admin): %v", err)
	}
	got, err := repo.FindByID(testCtx(t), member.ID)
	if err != nil {
		t.Fatalf("FindByID after promote: %v", err)
	}
	if got.Role != domain.RoleAdmin {
		t.Errorf("Role after promote = %v, want RoleAdmin", got.Role)
	}

	if err := repo.SetRole(testCtx(t), member.ID, domain.RoleMember); err != nil {
		t.Fatalf("SetRole(member): %v", err)
	}
	got, err = repo.FindByID(testCtx(t), member.ID)
	if err != nil {
		t.Fatalf("FindByID after demote: %v", err)
	}
	if got.Role != domain.RoleMember {
		t.Errorf("Role after demote = %v, want RoleMember", got.Role)
	}
}

func TestSetRoleNotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.SetRole(testCtx(t), domain.NewUserID(), domain.RoleAdmin)
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("SetRole(unknown id) = %v, want ErrUserNotFound", err)
	}
}

func TestSetPasswordHash(t *testing.T) {
	repo := newTestRepo(t)
	u := seedUser(t, repo, "maya@example.com")

	const newHash = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$bmV3aGFzaA"
	if err := repo.SetPasswordHash(testCtx(t), u.ID, newHash); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}
	got, err := repo.FindByID(testCtx(t), u.ID)
	if err != nil {
		t.Fatalf("FindByID after SetPasswordHash: %v", err)
	}
	if got.PasswordHash != newHash {
		t.Errorf("PasswordHash after SetPasswordHash = %q, want %q", got.PasswordHash, newHash)
	}
}

func TestSetPasswordHashNotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.SetPasswordHash(testCtx(t), domain.NewUserID(), "hash")
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("SetPasswordHash(unknown id) = %v, want ErrUserNotFound", err)
	}
}

func TestCount(t *testing.T) {
	repo := newTestRepo(t)

	n, err := repo.Count(testCtx(t))
	if err != nil {
		t.Fatalf("Count on an empty database: %v", err)
	}
	if n != 0 {
		t.Errorf("Count on an empty database = %d, want 0", n)
	}

	seedUser(t, repo, "maya@example.com")
	seedUser(t, repo, "ivy@example.com")

	n, err = repo.Count(testCtx(t))
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 2 {
		t.Errorf("Count = %d, want 2", n)
	}
}

func TestHasAnyUser(t *testing.T) {
	repo := newTestRepo(t)

	has, err := repo.HasAnyUser(testCtx(t))
	if err != nil {
		t.Fatalf("HasAnyUser on an empty database: %v", err)
	}
	if has {
		t.Error("HasAnyUser on an empty database = true, want false")
	}

	seedUser(t, repo, "maya@example.com")

	has, err = repo.HasAnyUser(testCtx(t))
	if err != nil {
		t.Fatalf("HasAnyUser: %v", err)
	}
	if !has {
		t.Error("HasAnyUser after seeding a user = false, want true")
	}
}
