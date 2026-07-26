package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ericfisherdev/nestcore/crypto/cryptotest"

	"github.com/ericfisherdev/nestorage/internal/identity/app"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

const testCurrentPassword = "correct-horse-battery-staple"

// fakePasswordRepo is an in-memory passwordChanger fake for PasswordService's
// hermetic unit tests. findErr lets a test simulate FindByID failing (e.g.
// domain.ErrUserNotFound) without a real database. order, when non-nil,
// records "set_password_hash" the moment SetPasswordHash runs, so a test can
// assert it happened before RevokeAll (see
// TestPasswordService_ChangeOwn_Succeeds).
type fakePasswordRepo struct {
	users map[domain.UserID]*domain.User

	findErr error

	order *[]string
}

func newFakePasswordRepo() *fakePasswordRepo {
	return &fakePasswordRepo{users: make(map[domain.UserID]*domain.User)}
}

func (f *fakePasswordRepo) FindByID(_ context.Context, id domain.UserID) (*domain.User, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	u, ok := f.users[id]
	if !ok {
		return nil, domain.ErrUserNotFound
	}
	return u, nil
}

func (f *fakePasswordRepo) SetPasswordHash(_ context.Context, id domain.UserID, hash string) error {
	if f.order != nil {
		*f.order = append(*f.order, "set_password_hash")
	}
	if u, ok := f.users[id]; ok {
		u.PasswordHash = hash
	}
	return nil
}

// orderedRevoker is an app.CredentialRevoker fake that records "revoke_all"
// into the same shared order slice fakePasswordRepo.SetPasswordHash writes
// to, so TestPasswordService_ChangeOwn_Succeeds can assert RevokeAll ran
// AFTER the write, not merely that both ran.
type orderedRevoker struct {
	order *[]string
	calls []domain.UserID
}

func (r *orderedRevoker) RevokeAll(_ context.Context, id domain.UserID) error {
	r.calls = append(r.calls, id)
	if r.order != nil {
		*r.order = append(*r.order, "revoke_all")
	}
	return nil
}

// newActiveUser seeds repo with an active user whose stored hash verifies
// against testCurrentPassword, and returns its id.
func newActiveUser(t *testing.T, repo *fakePasswordRepo) domain.UserID {
	t.Helper()
	hash, err := cryptotest.Hasher().Hash(testCurrentPassword)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	id := domain.NewUserID()
	repo.users[id] = &domain.User{ID: id, Active: true, PasswordHash: hash}
	return id
}

func TestNewPasswordService_NilDependenciesPanic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		fn   func()
	}{
		{"nil repo", func() { app.NewPasswordService(nil, cryptotest.Hasher(), &fakeRevoker{}, testLogger()) }},
		{"nil hasher", func() { app.NewPasswordService(newFakePasswordRepo(), nil, &fakeRevoker{}, testLogger()) }},
		{"nil revoker", func() { app.NewPasswordService(newFakePasswordRepo(), cryptotest.Hasher(), nil, testLogger()) }},
		{"nil logger", func() { app.NewPasswordService(newFakePasswordRepo(), cryptotest.Hasher(), &fakeRevoker{}, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recover() == nil {
					t.Errorf("NewPasswordService(%s) did not panic", tt.name)
				}
			}()
			tt.fn()
		})
	}
}

// TestPasswordService_ChangeOwn_Succeeds is the automated equivalent of this
// ticket's "a signed-in member can change their own password" criterion: the
// new hash lands, and RevokeAll runs exactly once, after the write.
func TestPasswordService_ChangeOwn_Succeeds(t *testing.T) {
	t.Parallel()
	var order []string
	repo := newFakePasswordRepo()
	repo.order = &order
	id := newActiveUser(t, repo)
	oldHash := repo.users[id].PasswordHash
	revoker := &orderedRevoker{order: &order}
	svc := app.NewPasswordService(repo, cryptotest.Hasher(), revoker, testLogger())

	if err := svc.ChangeOwn(context.Background(), id, testCurrentPassword, "a-new-correct-horse-battery"); err != nil {
		t.Fatalf("ChangeOwn: %v", err)
	}
	if repo.users[id].PasswordHash == oldHash {
		t.Error("ChangeOwn did not change the stored hash")
	}
	if len(revoker.calls) != 1 || revoker.calls[0] != id {
		t.Errorf("revoker calls = %v, want exactly one call for %v", revoker.calls, id)
	}
	if want := []string{"set_password_hash", "revoke_all"}; len(order) != 2 || order[0] != want[0] || order[1] != want[1] {
		t.Errorf("call order = %v, want %v (the write must land before revocation runs)", order, want)
	}
}

func TestPasswordService_ChangeOwn_WrongCurrentPasswordRefused(t *testing.T) {
	t.Parallel()
	repo := newFakePasswordRepo()
	id := newActiveUser(t, repo)
	oldHash := repo.users[id].PasswordHash
	revoker := &fakeRevoker{}
	svc := app.NewPasswordService(repo, cryptotest.Hasher(), revoker, testLogger())

	err := svc.ChangeOwn(context.Background(), id, "totally-wrong-password", "a-new-correct-horse-battery")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("ChangeOwn(wrong current password) error = %v, want ErrInvalidCredentials", err)
	}
	if repo.users[id].PasswordHash != oldHash {
		t.Error("ChangeOwn must not alter the stored hash when the current password check fails")
	}
	if len(revoker.calls) != 0 {
		t.Error("ChangeOwn must not revoke credentials when the current password check itself failed")
	}
}

func TestPasswordService_ChangeOwn_InactiveUserRefused(t *testing.T) {
	t.Parallel()
	repo := newFakePasswordRepo()
	id := newActiveUser(t, repo)
	repo.users[id].Active = false
	oldHash := repo.users[id].PasswordHash
	revoker := &fakeRevoker{}
	svc := app.NewPasswordService(repo, cryptotest.Hasher(), revoker, testLogger())

	err := svc.ChangeOwn(context.Background(), id, testCurrentPassword, "a-new-correct-horse-battery")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("ChangeOwn(inactive user) error = %v, want ErrInvalidCredentials", err)
	}
	if repo.users[id].PasswordHash != oldHash {
		t.Error("ChangeOwn must not alter the stored hash for an inactive user")
	}
	if len(revoker.calls) != 0 {
		t.Error("ChangeOwn must not revoke credentials when the user is inactive")
	}
}

func TestPasswordService_ChangeOwn_UnknownIDWrapsErrUserNotFound(t *testing.T) {
	t.Parallel()
	repo := newFakePasswordRepo()
	revoker := &fakeRevoker{}
	svc := app.NewPasswordService(repo, cryptotest.Hasher(), revoker, testLogger())

	err := svc.ChangeOwn(context.Background(), domain.NewUserID(), testCurrentPassword, "a-new-correct-horse-battery")
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("ChangeOwn(unknown id) error = %v, want ErrUserNotFound", err)
	}
	if len(revoker.calls) != 0 {
		t.Error("ChangeOwn must not revoke credentials when the user lookup itself failed")
	}
}

func TestPasswordService_ChangeOwn_ValidatesNewPasswordTooShort(t *testing.T) {
	t.Parallel()
	repo := newFakePasswordRepo()
	id := newActiveUser(t, repo)
	oldHash := repo.users[id].PasswordHash
	revoker := &fakeRevoker{}
	svc := app.NewPasswordService(repo, cryptotest.Hasher(), revoker, testLogger())

	err := svc.ChangeOwn(context.Background(), id, testCurrentPassword, "short")
	if !errors.Is(err, domain.ErrPasswordTooShort) {
		t.Errorf("ChangeOwn(short new password) error = %v, want ErrPasswordTooShort", err)
	}
	if repo.users[id].PasswordHash != oldHash {
		t.Error("ChangeOwn must not alter the stored hash when the new password fails validation")
	}
	if len(revoker.calls) != 0 {
		t.Error("ChangeOwn must not revoke credentials when the new password fails validation")
	}
}

func TestPasswordService_ChangeOwn_ValidatesNewPasswordTooLong(t *testing.T) {
	t.Parallel()
	repo := newFakePasswordRepo()
	id := newActiveUser(t, repo)
	oldHash := repo.users[id].PasswordHash
	revoker := &fakeRevoker{}
	svc := app.NewPasswordService(repo, cryptotest.Hasher(), revoker, testLogger())

	tooLong := make([]byte, 129)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	err := svc.ChangeOwn(context.Background(), id, testCurrentPassword, string(tooLong))
	if !errors.Is(err, domain.ErrPasswordTooLong) {
		t.Errorf("ChangeOwn(long new password) error = %v, want ErrPasswordTooLong", err)
	}
	if repo.users[id].PasswordHash != oldHash {
		t.Error("ChangeOwn must not alter the stored hash when the new password fails validation")
	}
	if len(revoker.calls) != 0 {
		t.Error("ChangeOwn must not revoke credentials when the new password fails validation")
	}
}
