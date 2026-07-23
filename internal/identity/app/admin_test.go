package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/ericfisherdev/nestcore/crypto/cryptotest"

	"github.com/ericfisherdev/nestorage/internal/identity/app"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// fakeAdminRepo is an in-memory userRepository fake for AdminService's
// hermetic unit tests. The three *Err fields let a test simulate a
// repository failure (e.g. domain.ErrLastActiveAdmin) for exactly the
// method under test, without needing a real transaction or database.
type fakeAdminRepo struct {
	users map[domain.UserID]*domain.User

	setRoleErr         error
	setActiveErr       error
	setPasswordHashErr error
}

func newFakeAdminRepo() *fakeAdminRepo {
	return &fakeAdminRepo{users: make(map[domain.UserID]*domain.User)}
}

func (f *fakeAdminRepo) Create(_ context.Context, u *domain.User) error {
	f.users[u.ID] = u
	return nil
}

func (f *fakeAdminRepo) List(_ context.Context) ([]domain.User, error) {
	users := make([]domain.User, 0, len(f.users))
	for _, u := range f.users {
		users = append(users, *u)
	}
	return users, nil
}

func (f *fakeAdminRepo) SetRole(_ context.Context, id domain.UserID, role domain.Role) error {
	if f.setRoleErr != nil {
		return f.setRoleErr
	}
	if u, ok := f.users[id]; ok {
		u.Role = role
	}
	return nil
}

func (f *fakeAdminRepo) SetActive(_ context.Context, id domain.UserID, active bool) error {
	if f.setActiveErr != nil {
		return f.setActiveErr
	}
	if u, ok := f.users[id]; ok {
		u.Active = active
	}
	return nil
}

func (f *fakeAdminRepo) SetPasswordHash(_ context.Context, id domain.UserID, hash string) error {
	if f.setPasswordHashErr != nil {
		return f.setPasswordHashErr
	}
	if u, ok := f.users[id]; ok {
		u.PasswordHash = hash
	}
	return nil
}

// fakeRevoker is a configurable app.CredentialRevoker fake: err makes
// RevokeAll fail, and calls records every id it was asked to revoke, so
// tests can assert revocation happened (or did not).
type fakeRevoker struct {
	err   error
	calls []domain.UserID
}

func (f *fakeRevoker) RevokeAll(_ context.Context, id domain.UserID) error {
	f.calls = append(f.calls, id)
	return f.err
}

func newAdminService(repo *fakeAdminRepo, revoker *fakeRevoker) *app.AdminService {
	return app.NewAdminService(repo, cryptotest.Hasher(), revoker, testLogger())
}

func TestNewAdminService_NilDependenciesPanic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		fn   func()
	}{
		{"nil repo", func() { app.NewAdminService(nil, cryptotest.Hasher(), &fakeRevoker{}, testLogger()) }},
		{"nil hasher", func() { app.NewAdminService(newFakeAdminRepo(), nil, &fakeRevoker{}, testLogger()) }},
		{"nil revoker", func() { app.NewAdminService(newFakeAdminRepo(), cryptotest.Hasher(), nil, testLogger()) }},
		{"nil logger", func() { app.NewAdminService(newFakeAdminRepo(), cryptotest.Hasher(), &fakeRevoker{}, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recover() == nil {
					t.Errorf("NewAdminService(%s) did not panic", tt.name)
				}
			}()
			tt.fn()
		})
	}
}

func TestAdminService_Create_Succeeds(t *testing.T) {
	t.Parallel()
	repo := newFakeAdminRepo()
	svc := newAdminService(repo, &fakeRevoker{})

	u, err := svc.Create(context.Background(), "Maya", "maya@example.com", "correct-horse-battery-staple", domain.RoleMember, domain.ColorIndigo)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if u.PasswordHash == "" {
		t.Error("Create left PasswordHash empty")
	}
	if _, ok := repo.users[u.ID]; !ok {
		t.Error("Create did not persist the user via the repository")
	}
}

func TestAdminService_Create_ValidatesPassword(t *testing.T) {
	t.Parallel()
	svc := newAdminService(newFakeAdminRepo(), &fakeRevoker{})

	_, err := svc.Create(context.Background(), "Maya", "maya@example.com", "short", domain.RoleMember, domain.ColorIndigo)
	if !errors.Is(err, domain.ErrPasswordTooShort) {
		t.Errorf("Create(short password) error = %v, want ErrPasswordTooShort", err)
	}
}

// TestAdminService_Deactivate_RevokesCredentials is the automated
// equivalent of this ticket's "deactivating a user immediately invalidates
// their sessions" criterion, at the AdminService layer: the flag flips and
// the revoker is called exactly once, for the right user.
func TestAdminService_Deactivate_RevokesCredentials(t *testing.T) {
	t.Parallel()
	id := domain.NewUserID()
	repo := newFakeAdminRepo()
	repo.users[id] = &domain.User{ID: id, Active: true}
	revoker := &fakeRevoker{}
	svc := newAdminService(repo, revoker)

	if err := svc.Deactivate(context.Background(), id); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	if repo.users[id].Active {
		t.Error("Deactivate left Active = true, want false")
	}
	if len(revoker.calls) != 1 || revoker.calls[0] != id {
		t.Errorf("revoker calls = %v, want exactly one call for %v", revoker.calls, id)
	}
}

// TestAdminService_Deactivate_RevokerErrorSurfaces asserts a revocation
// failure is returned to the caller, not swallowed — the user IS
// deactivated (the flag flip already committed), but the caller has to see
// that a credential may have survived.
func TestAdminService_Deactivate_RevokerErrorSurfaces(t *testing.T) {
	t.Parallel()
	id := domain.NewUserID()
	repo := newFakeAdminRepo()
	repo.users[id] = &domain.User{ID: id, Active: true}
	wantErr := errors.New("revoke boom")
	svc := newAdminService(repo, &fakeRevoker{err: wantErr})

	err := svc.Deactivate(context.Background(), id)
	if !errors.Is(err, wantErr) {
		t.Errorf("Deactivate error = %v, want it to wrap %v", err, wantErr)
	}
	if repo.users[id].Active {
		t.Error("Deactivate left Active = true after a revoker failure, want false (the flag flip must not roll back)")
	}
}

// TestAdminService_Deactivate_LastActiveAdminPropagatesUnchanged asserts the
// repository's domain.ErrLastActiveAdmin reaches the caller unchanged (via
// errors.Is through the wrap), and that a rejected deactivation never
// reaches the revoker at all.
func TestAdminService_Deactivate_LastActiveAdminPropagatesUnchanged(t *testing.T) {
	t.Parallel()
	id := domain.NewUserID()
	repo := newFakeAdminRepo()
	repo.users[id] = &domain.User{ID: id, Role: domain.RoleAdmin, Active: true}
	repo.setActiveErr = domain.ErrLastActiveAdmin
	revoker := &fakeRevoker{}
	svc := newAdminService(repo, revoker)

	err := svc.Deactivate(context.Background(), id)
	if !errors.Is(err, domain.ErrLastActiveAdmin) {
		t.Errorf("Deactivate error = %v, want ErrLastActiveAdmin", err)
	}
	if len(revoker.calls) != 0 {
		t.Error("Deactivate must not revoke credentials when the flag flip itself was rejected")
	}
}

func TestAdminService_ChangeRole_LastActiveAdminPropagatesUnchanged(t *testing.T) {
	t.Parallel()
	id := domain.NewUserID()
	repo := newFakeAdminRepo()
	repo.users[id] = &domain.User{ID: id, Role: domain.RoleAdmin, Active: true}
	repo.setRoleErr = domain.ErrLastActiveAdmin
	svc := newAdminService(repo, &fakeRevoker{})

	err := svc.ChangeRole(context.Background(), id, domain.RoleMember)
	if !errors.Is(err, domain.ErrLastActiveAdmin) {
		t.Errorf("ChangeRole error = %v, want ErrLastActiveAdmin", err)
	}
}

// TestAdminService_ResetPassword_RehashesAndRevokes is the automated
// equivalent of "an admin resetting someone's password revokes their
// outstanding credentials".
func TestAdminService_ResetPassword_RehashesAndRevokes(t *testing.T) {
	t.Parallel()
	id := domain.NewUserID()
	repo := newFakeAdminRepo()
	const oldHash = "old-hash"
	repo.users[id] = &domain.User{ID: id, PasswordHash: oldHash}
	revoker := &fakeRevoker{}
	svc := newAdminService(repo, revoker)

	if err := svc.ResetPassword(context.Background(), id, "correct-horse-battery-staple"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if repo.users[id].PasswordHash == oldHash {
		t.Error("ResetPassword did not change the stored hash")
	}
	if len(revoker.calls) != 1 || revoker.calls[0] != id {
		t.Errorf("revoker calls = %v, want exactly one call for %v", revoker.calls, id)
	}
}

func TestAdminService_ResetPassword_ValidatesPassword(t *testing.T) {
	t.Parallel()
	id := domain.NewUserID()
	repo := newFakeAdminRepo()
	repo.users[id] = &domain.User{ID: id}
	revoker := &fakeRevoker{}
	svc := newAdminService(repo, revoker)

	err := svc.ResetPassword(context.Background(), id, "short")
	if !errors.Is(err, domain.ErrPasswordTooShort) {
		t.Errorf("ResetPassword(short password) error = %v, want ErrPasswordTooShort", err)
	}
	if len(revoker.calls) != 0 {
		t.Error("ResetPassword must not revoke credentials when validation itself failed")
	}
}

func TestAdminService_Reactivate_Succeeds(t *testing.T) {
	t.Parallel()
	id := domain.NewUserID()
	repo := newFakeAdminRepo()
	repo.users[id] = &domain.User{ID: id, Active: false}
	svc := newAdminService(repo, &fakeRevoker{})

	if err := svc.Reactivate(context.Background(), id); err != nil {
		t.Fatalf("Reactivate: %v", err)
	}
	if !repo.users[id].Active {
		t.Error("Reactivate left Active = false, want true")
	}
}
