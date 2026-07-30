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

// fakeHouseholds is a configurable householdResolver fake. By default it
// reports exactly one household (the steady-state after first-run setup);
// tests override households or err to exercise the zero/many/error paths.
type fakeHouseholds struct {
	households []domain.Household
	err        error
}

func newFakeHouseholds() *fakeHouseholds {
	return &fakeHouseholds{households: []domain.Household{{ID: domain.NewHouseholdID(), Name: "Household"}}}
}

func (f *fakeHouseholds) List(_ context.Context) ([]domain.Household, error) {
	return f.households, f.err
}

func newAdminService(repo *fakeAdminRepo, revoker *fakeRevoker) *app.AdminService {
	return app.NewAdminService(repo, newFakeHouseholds(), cryptotest.Hasher(), revoker, testLogger())
}

func TestNewAdminService_NilDependenciesPanic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		fn   func()
	}{
		{"nil repo", func() {
			app.NewAdminService(nil, newFakeHouseholds(), cryptotest.Hasher(), &fakeRevoker{}, testLogger())
		}},
		{"nil households", func() {
			app.NewAdminService(newFakeAdminRepo(), nil, cryptotest.Hasher(), &fakeRevoker{}, testLogger())
		}},
		{"nil hasher", func() {
			app.NewAdminService(newFakeAdminRepo(), newFakeHouseholds(), nil, &fakeRevoker{}, testLogger())
		}},
		{"nil revoker", func() {
			app.NewAdminService(newFakeAdminRepo(), newFakeHouseholds(), cryptotest.Hasher(), nil, testLogger())
		}},
		{"nil logger", func() {
			app.NewAdminService(newFakeAdminRepo(), newFakeHouseholds(), cryptotest.Hasher(), &fakeRevoker{}, nil)
		}},
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

func TestAdminService_List_Succeeds(t *testing.T) {
	t.Parallel()
	repo := newFakeAdminRepo()
	repo.users[domain.NewUserID()] = &domain.User{DisplayName: "Maya"}
	repo.users[domain.NewUserID()] = &domain.User{DisplayName: "Daniel"}
	svc := newAdminService(repo, &fakeRevoker{})

	got, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("List returned %d users, want 2", len(got))
	}
}

// fakeAdminRepoListErr wraps fakeAdminRepo to force List to fail, since
// fakeAdminRepo itself has no listErr field — List's only failure mode is a
// repository error, which this thin wrapper is the simplest way to inject.
type fakeAdminRepoListErr struct {
	*fakeAdminRepo
	err error
}

func (f *fakeAdminRepoListErr) List(context.Context) ([]domain.User, error) {
	return nil, f.err
}

func TestAdminService_List_RepositoryErrorWrapped(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("list boom")
	repo := &fakeAdminRepoListErr{fakeAdminRepo: newFakeAdminRepo(), err: wantErr}
	svc := app.NewAdminService(repo, newFakeHouseholds(), cryptotest.Hasher(), &fakeRevoker{}, testLogger())

	_, err := svc.List(context.Background())
	if !errors.Is(err, wantErr) {
		t.Errorf("List error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestAdminService_Create_Succeeds(t *testing.T) {
	t.Parallel()
	repo := newFakeAdminRepo()
	households := newFakeHouseholds()
	svc := app.NewAdminService(repo, households, cryptotest.Hasher(), &fakeRevoker{}, testLogger())

	u, err := svc.Create(context.Background(), "Maya", "maya@example.com", "correct-horse-battery-staple", domain.RoleAdult, domain.ColorIndigo)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if u.PasswordHash == "" {
		t.Error("Create left PasswordHash empty")
	}
	if u.HouseholdID != households.households[0].ID {
		t.Errorf("Create HouseholdID = %v, want the sole existing household %v", u.HouseholdID, households.households[0].ID)
	}
	if _, ok := repo.users[u.ID]; !ok {
		t.Error("Create did not persist the user via the repository")
	}
}

func TestAdminService_Create_ValidatesPassword(t *testing.T) {
	t.Parallel()
	svc := newAdminService(newFakeAdminRepo(), &fakeRevoker{})

	_, err := svc.Create(context.Background(), "Maya", "maya@example.com", "short", domain.RoleAdult, domain.ColorIndigo)
	if !errors.Is(err, domain.ErrPasswordTooShort) {
		t.Errorf("Create(short password) error = %v, want ErrPasswordTooShort", err)
	}
}

// TestAdminService_Create_NoHouseholdFails asserts Create refuses to invent a
// household (NSTR-116's adopt-only rule): zero existing households is a
// genuine invariant violation once first-run setup should already have run.
func TestAdminService_Create_NoHouseholdFails(t *testing.T) {
	t.Parallel()
	repo := newFakeAdminRepo()
	svc := app.NewAdminService(repo, &fakeHouseholds{}, cryptotest.Hasher(), &fakeRevoker{}, testLogger())

	_, err := svc.Create(context.Background(), "Maya", "maya@example.com", "correct-horse-battery-staple", domain.RoleAdult, domain.ColorIndigo)
	if !errors.Is(err, domain.ErrNoHousehold) {
		t.Errorf("Create(no households) error = %v, want ErrNoHousehold", err)
	}
	if len(repo.users) != 0 {
		t.Error("Create must not persist a user when household resolution fails")
	}
}

// TestAdminService_Create_AmbiguousHouseholdFails asserts Create fails loudly
// rather than guessing when several households exist.
func TestAdminService_Create_AmbiguousHouseholdFails(t *testing.T) {
	t.Parallel()
	repo := newFakeAdminRepo()
	households := &fakeHouseholds{households: []domain.Household{
		{ID: domain.NewHouseholdID()},
		{ID: domain.NewHouseholdID()},
	}}
	svc := app.NewAdminService(repo, households, cryptotest.Hasher(), &fakeRevoker{}, testLogger())

	_, err := svc.Create(context.Background(), "Maya", "maya@example.com", "correct-horse-battery-staple", domain.RoleAdult, domain.ColorIndigo)
	if !errors.Is(err, domain.ErrAmbiguousHousehold) {
		t.Errorf("Create(ambiguous households) error = %v, want ErrAmbiguousHousehold", err)
	}
	if len(repo.users) != 0 {
		t.Error("Create must not persist a user when household resolution fails")
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
	repo.users[id] = &domain.User{ID: id, Role: domain.RoleOwner, Active: true}
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

func TestAdminService_ChangeRole_Succeeds(t *testing.T) {
	t.Parallel()
	id := domain.NewUserID()
	repo := newFakeAdminRepo()
	repo.users[id] = &domain.User{ID: id, Role: domain.RoleAdult, Active: true}
	svc := newAdminService(repo, &fakeRevoker{})

	if err := svc.ChangeRole(context.Background(), id, domain.RoleOwner); err != nil {
		t.Fatalf("ChangeRole: %v", err)
	}
	if repo.users[id].Role != domain.RoleOwner {
		t.Errorf("Role after ChangeRole = %v, want RoleOwner", repo.users[id].Role)
	}
}

func TestAdminService_ChangeRole_LastActiveAdminPropagatesUnchanged(t *testing.T) {
	t.Parallel()
	id := domain.NewUserID()
	repo := newFakeAdminRepo()
	repo.users[id] = &domain.User{ID: id, Role: domain.RoleOwner, Active: true}
	repo.setRoleErr = domain.ErrLastActiveAdmin
	svc := newAdminService(repo, &fakeRevoker{})

	err := svc.ChangeRole(context.Background(), id, domain.RoleAdult)
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
