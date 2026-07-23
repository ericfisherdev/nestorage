package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ericfisherdev/nestorage/internal/identity/app"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// fixedClock is a deterministic clock func for DeviceTokenService tests, so
// assertions never depend on wall-clock time.
func fixedClock() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

// fakeDeviceTokenRepo is an in-memory deviceTokenRepository fake. The *Err
// fields let a test simulate a repository failure for exactly the method
// under test, mirroring fakeAdminRepo's own shape.
type fakeDeviceTokenRepo struct {
	byHash map[string]*domain.DeviceToken
	byID   map[domain.DeviceTokenID]*domain.DeviceToken

	createErr         error
	getByTokenHashErr error
	listErr           error
	revokeErr         error
	revokeAllErr      error
	touchErr          error

	touchCalls []domain.DeviceTokenID
}

func newFakeDeviceTokenRepo() *fakeDeviceTokenRepo {
	return &fakeDeviceTokenRepo{
		byHash: make(map[string]*domain.DeviceToken),
		byID:   make(map[domain.DeviceTokenID]*domain.DeviceToken),
	}
}

func (f *fakeDeviceTokenRepo) Create(_ context.Context, t *domain.DeviceToken) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.byHash[t.TokenHash] = t
	f.byID[t.ID] = t
	return nil
}

func (f *fakeDeviceTokenRepo) GetByTokenHash(_ context.Context, hash string) (*domain.DeviceToken, error) {
	if f.getByTokenHashErr != nil {
		return nil, f.getByTokenHashErr
	}
	t, ok := f.byHash[hash]
	if !ok {
		return nil, domain.ErrDeviceTokenNotFound
	}
	return t, nil
}

func (f *fakeDeviceTokenRepo) ListByUser(_ context.Context, userID domain.UserID) ([]*domain.DeviceToken, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]*domain.DeviceToken, 0)
	for _, t := range f.byID {
		if t.UserID == userID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeDeviceTokenRepo) Revoke(_ context.Context, userID domain.UserID, id domain.DeviceTokenID, revokedAt time.Time) error {
	if f.revokeErr != nil {
		return f.revokeErr
	}
	t, ok := f.byID[id]
	if !ok || t.UserID != userID {
		return domain.ErrDeviceTokenNotFound
	}
	t.RevokedAt = &revokedAt
	return nil
}

func (f *fakeDeviceTokenRepo) RevokeAllForUser(_ context.Context, userID domain.UserID, revokedAt time.Time) (int64, error) {
	if f.revokeAllErr != nil {
		return 0, f.revokeAllErr
	}
	var n int64
	for _, t := range f.byID {
		if t.UserID == userID && t.RevokedAt == nil {
			t.RevokedAt = &revokedAt
			n++
		}
	}
	return n, nil
}

func (f *fakeDeviceTokenRepo) TouchLastUsed(_ context.Context, id domain.DeviceTokenID, now, _ time.Time) error {
	f.touchCalls = append(f.touchCalls, id)
	if f.touchErr != nil {
		return f.touchErr
	}
	if t, ok := f.byID[id]; ok {
		t.LastUsedAt = &now
	}
	return nil
}

// fakeOwnerFinder is an in-memory deviceOwnerFinder fake.
type fakeOwnerFinder struct {
	users map[domain.UserID]*domain.User
	err   error
}

func (f *fakeOwnerFinder) FindByID(_ context.Context, id domain.UserID) (*domain.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	u, ok := f.users[id]
	if !ok {
		return nil, domain.ErrUserNotFound
	}
	return u, nil
}

// fakeCredentialVerifier is a configurable credentialVerifier fake: err
// makes Login fail, and calls records how many times it was invoked, so a
// test can assert the limiter short-circuited before it was ever touched.
type fakeCredentialVerifier struct {
	userID domain.UserID
	err    error
	calls  int
}

func (f *fakeCredentialVerifier) Login(_ context.Context, _, _ string) (domain.UserID, error) {
	f.calls++
	if f.err != nil {
		return domain.UserID{}, f.err
	}
	return f.userID, nil
}

// fakeAttemptLimiter is a configurable attemptLimiter fake.
type fakeAttemptLimiter struct {
	locked           bool
	recordFailureRet bool
	failureCalls     int
	successCalls     int
}

func (f *fakeAttemptLimiter) Locked(string, time.Time) bool { return f.locked }

func (f *fakeAttemptLimiter) RecordFailure(string, time.Time) bool {
	f.failureCalls++
	return f.recordFailureRet
}

func (f *fakeAttemptLimiter) RecordSuccess(string) { f.successCalls++ }

// deviceTokenFixture bundles every fake dependency DeviceTokenService needs,
// so each test only overrides the field it cares about.
type deviceTokenFixture struct {
	tokens  *fakeDeviceTokenRepo
	users   *fakeOwnerFinder
	authn   *fakeCredentialVerifier
	limiter *fakeAttemptLimiter
}

func newDeviceTokenFixture() *deviceTokenFixture {
	return &deviceTokenFixture{
		tokens:  newFakeDeviceTokenRepo(),
		users:   &fakeOwnerFinder{users: make(map[domain.UserID]*domain.User)},
		authn:   &fakeCredentialVerifier{},
		limiter: &fakeAttemptLimiter{},
	}
}

func (f *deviceTokenFixture) service() *app.DeviceTokenService {
	return app.NewDeviceTokenService(f.tokens, f.users, f.authn, f.limiter, fixedClock, testLogger())
}

func TestNewDeviceTokenService_NilDependenciesPanic(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	tests := []struct {
		name string
		fn   func()
	}{
		{"nil tokens repo", func() {
			app.NewDeviceTokenService(nil, f.users, f.authn, f.limiter, fixedClock, testLogger())
		}},
		{"nil users finder", func() {
			app.NewDeviceTokenService(f.tokens, nil, f.authn, f.limiter, fixedClock, testLogger())
		}},
		{"nil authn", func() {
			app.NewDeviceTokenService(f.tokens, f.users, nil, f.limiter, fixedClock, testLogger())
		}},
		{"nil limiter", func() {
			app.NewDeviceTokenService(f.tokens, f.users, f.authn, nil, fixedClock, testLogger())
		}},
		{"nil clock", func() {
			app.NewDeviceTokenService(f.tokens, f.users, f.authn, f.limiter, nil, testLogger())
		}},
		{"nil logger", func() {
			app.NewDeviceTokenService(f.tokens, f.users, f.authn, f.limiter, fixedClock, nil)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recover() == nil {
					t.Errorf("NewDeviceTokenService(%s) did not panic", tt.name)
				}
			}()
			tt.fn()
		})
	}
}

// TestDeviceTokenService_Issue_ReturnsPlaintextOnceAndStoresOnlyHash is the
// automated equivalent of this ticket's "the plaintext secret appears
// exactly once, at creation, and is not recoverable afterwards" criterion.
func TestDeviceTokenService_Issue_ReturnsPlaintextOnceAndStoresOnlyHash(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	userID := domain.NewUserID()
	f.authn.userID = userID
	svc := f.service()

	plaintext, token, err := svc.Issue(context.Background(), "maya@example.com", "correct-horse", "Maya's phone")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if plaintext == "" {
		t.Fatal("Issue returned an empty plaintext")
	}
	if token.TokenHash == plaintext {
		t.Error("the persisted TokenHash equals the returned plaintext — the raw token must never be stored")
	}
	if token.TokenHash != domain.HashDeviceToken(plaintext) {
		t.Error("the persisted TokenHash does not match HashDeviceToken(plaintext)")
	}
	if token.UserID != userID {
		t.Errorf("token.UserID = %v, want %v", token.UserID, userID)
	}
	stored, ok := f.tokens.byID[token.ID]
	if !ok {
		t.Fatal("Issue did not persist the token via the repository")
	}
	if stored.TokenHash == plaintext {
		t.Error("the repository's stored copy holds the plaintext instead of its hash")
	}
}

func TestDeviceTokenService_Issue_TrimsDeviceName(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	svc := f.service()

	_, token, err := svc.Issue(context.Background(), "maya@example.com", "correct-horse", "  Maya's phone  ")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if token.Name != "Maya's phone" {
		t.Errorf("token.Name = %q, want trimmed %q", token.Name, "Maya's phone")
	}
}

func TestDeviceTokenService_Issue_RejectsBlankDeviceName(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	svc := f.service()

	_, _, err := svc.Issue(context.Background(), "maya@example.com", "correct-horse", "   ")
	if !errors.Is(err, domain.ErrInvalidDeviceToken) {
		t.Errorf("Issue(blank name) error = %v, want wrapped ErrInvalidDeviceToken", err)
	}
	if f.authn.calls != 0 {
		t.Error("Issue must reject a blank device name before ever touching the credential verifier")
	}
}

func TestDeviceTokenService_Issue_RejectsOverLongDeviceName(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	svc := f.service()

	name := ""
	for range 101 {
		name += "a"
	}
	_, _, err := svc.Issue(context.Background(), "maya@example.com", "correct-horse", name)
	if !errors.Is(err, domain.ErrInvalidDeviceToken) {
		t.Errorf("Issue(101-char name) error = %v, want wrapped ErrInvalidDeviceToken", err)
	}
}

// TestDeviceTokenService_Issue_LockedOutSkipsAuthentication asserts the
// limiter is checked BEFORE the credential verifier is ever touched — the
// same ordering NSTR-20's login Handlers uses.
func TestDeviceTokenService_Issue_LockedOutSkipsAuthentication(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	f.limiter.locked = true
	svc := f.service()

	_, _, err := svc.Issue(context.Background(), "maya@example.com", "wrong", "phone")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("Issue(locked out) error = %v, want ErrInvalidCredentials", err)
	}
	if f.authn.calls != 0 {
		t.Error("Issue must not call the credential verifier once the limiter reports locked")
	}
}

// TestDeviceTokenService_Issue_WrongCredentialsRecordFailure asserts a
// credential failure is reported to the SAME limiter NSTR-20's login uses
// (via the injected attemptLimiter port) — see attemptLimiter's own doc for
// why sharing it matters.
func TestDeviceTokenService_Issue_WrongCredentialsRecordFailure(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	f.authn.err = domain.ErrInvalidCredentials
	f.limiter.recordFailureRet = true // simulate the attempt that crosses the lockout threshold
	svc := f.service()

	_, _, err := svc.Issue(context.Background(), "maya@example.com", "wrong", "phone")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("Issue(wrong credentials) error = %v, want ErrInvalidCredentials", err)
	}
	if f.limiter.failureCalls != 1 {
		t.Errorf("limiter.RecordFailure calls = %d, want 1", f.limiter.failureCalls)
	}
	if f.limiter.successCalls != 0 {
		t.Error("a failed login must not record a success")
	}
}

func TestDeviceTokenService_Issue_SuccessRecordsSuccess(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	svc := f.service()

	if _, _, err := svc.Issue(context.Background(), "maya@example.com", "correct-horse", "phone"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if f.limiter.successCalls != 1 {
		t.Errorf("limiter.RecordSuccess calls = %d, want 1", f.limiter.successCalls)
	}
}

func TestDeviceTokenService_Issue_AuthenticatorErrorWrapped(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	wantErr := errors.New("authn boom")
	f.authn.err = wantErr
	svc := f.service()

	_, _, err := svc.Issue(context.Background(), "maya@example.com", "correct-horse", "phone")
	if !errors.Is(err, wantErr) {
		t.Errorf("Issue error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestDeviceTokenService_Issue_RepositoryErrorWrapped(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	wantErr := errors.New("create boom")
	f.tokens.createErr = wantErr
	svc := f.service()

	_, _, err := svc.Issue(context.Background(), "maya@example.com", "correct-horse", "phone")
	if !errors.Is(err, wantErr) {
		t.Errorf("Issue error = %v, want it to wrap %v", err, wantErr)
	}
}

func seedActiveToken(f *deviceTokenFixture, userID domain.UserID) (raw string, token *domain.DeviceToken) {
	raw, _ = domain.GenerateDeviceToken()
	token = &domain.DeviceToken{
		ID:        domain.NewDeviceTokenID(),
		UserID:    userID,
		TokenHash: domain.HashDeviceToken(raw),
		Name:      "phone",
	}
	f.tokens.byHash[token.TokenHash] = token
	f.tokens.byID[token.ID] = token
	return raw, token
}

func TestDeviceTokenService_Authenticate_Success(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	userID := domain.NewUserID()
	f.users.users[userID] = &domain.User{ID: userID, Active: true}
	raw, token := seedActiveToken(f, userID)
	svc := f.service()

	gotUser, gotToken, err := svc.Authenticate(context.Background(), raw)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if gotUser.ID != userID {
		t.Errorf("Authenticate user id = %v, want %v", gotUser.ID, userID)
	}
	if gotToken.ID != token.ID {
		t.Errorf("Authenticate token id = %v, want %v", gotToken.ID, token.ID)
	}
	if len(f.tokens.touchCalls) != 1 || f.tokens.touchCalls[0] != token.ID {
		t.Errorf("touch calls = %v, want exactly one call for %v", f.tokens.touchCalls, token.ID)
	}
}

func TestDeviceTokenService_Authenticate_UnknownToken(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	svc := f.service()

	_, _, err := svc.Authenticate(context.Background(), "nsd_unknown")
	if !errors.Is(err, domain.ErrDeviceTokenNotFound) {
		t.Errorf("Authenticate(unknown) error = %v, want ErrDeviceTokenNotFound", err)
	}
}

// TestDeviceTokenService_Authenticate_RevokedToken is the automated
// equivalent of this ticket's "a revoked token is rejected immediately"
// criterion, at the service layer.
func TestDeviceTokenService_Authenticate_RevokedToken(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	userID := domain.NewUserID()
	f.users.users[userID] = &domain.User{ID: userID, Active: true}
	raw, token := seedActiveToken(f, userID)
	revokedAt := fixedClock()
	token.RevokedAt = &revokedAt
	svc := f.service()

	_, _, err := svc.Authenticate(context.Background(), raw)
	if !errors.Is(err, domain.ErrDeviceTokenRevoked) {
		t.Errorf("Authenticate(revoked) error = %v, want ErrDeviceTokenRevoked", err)
	}
	if len(f.tokens.touchCalls) != 0 {
		t.Error("Authenticate must not touch last-used for a revoked token")
	}
}

func TestDeviceTokenService_Authenticate_InactiveOwner(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	userID := domain.NewUserID()
	f.users.users[userID] = &domain.User{ID: userID, Active: false}
	raw, _ := seedActiveToken(f, userID)
	svc := f.service()

	_, _, err := svc.Authenticate(context.Background(), raw)
	if !errors.Is(err, domain.ErrUserInactive) {
		t.Errorf("Authenticate(inactive owner) error = %v, want ErrUserInactive", err)
	}
}

func TestDeviceTokenService_Authenticate_OwnerNotFound(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	raw, _ := seedActiveToken(f, domain.NewUserID())
	svc := f.service()

	_, _, err := svc.Authenticate(context.Background(), raw)
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("Authenticate(unknown owner) error = %v, want ErrUserNotFound", err)
	}
}

func TestDeviceTokenService_Authenticate_RepositoryErrorWrapped(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	wantErr := errors.New("lookup boom")
	f.tokens.getByTokenHashErr = wantErr
	svc := f.service()

	_, _, err := svc.Authenticate(context.Background(), "nsd_whatever")
	if !errors.Is(err, wantErr) {
		t.Errorf("Authenticate error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestDeviceTokenService_Authenticate_OwnerLookupErrorWrapped(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	wantErr := errors.New("owner lookup boom")
	f.users.err = wantErr
	raw, _ := seedActiveToken(f, domain.NewUserID())
	svc := f.service()

	_, _, err := svc.Authenticate(context.Background(), raw)
	if !errors.Is(err, wantErr) {
		t.Errorf("Authenticate error = %v, want it to wrap %v", err, wantErr)
	}
}

// TestDeviceTokenService_Authenticate_TouchFailureIsSwallowed asserts a
// failed best-effort touch never fails an otherwise valid authentication.
func TestDeviceTokenService_Authenticate_TouchFailureIsSwallowed(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	userID := domain.NewUserID()
	f.users.users[userID] = &domain.User{ID: userID, Active: true}
	raw, _ := seedActiveToken(f, userID)
	f.tokens.touchErr = errors.New("touch boom")
	svc := f.service()

	if _, _, err := svc.Authenticate(context.Background(), raw); err != nil {
		t.Errorf("Authenticate = %v, want nil (a touch failure must be swallowed)", err)
	}
}

func TestDeviceTokenService_ListForUser(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	userID := domain.NewUserID()
	_, _ = seedActiveToken(f, userID)
	_, _ = seedActiveToken(f, domain.NewUserID())
	svc := f.service()

	got, err := svc.ListForUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("ListForUser returned %d tokens, want 1", len(got))
	}
}

func TestDeviceTokenService_ListForUser_RepositoryErrorWrapped(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	wantErr := errors.New("list boom")
	f.tokens.listErr = wantErr
	svc := f.service()

	_, err := svc.ListForUser(context.Background(), domain.NewUserID())
	if !errors.Is(err, wantErr) {
		t.Errorf("ListForUser error = %v, want it to wrap %v", err, wantErr)
	}
}

// TestDeviceTokenService_Revoke_ScopedByUser is the automated equivalent of
// this ticket's "revoking one device does not affect that user's other
// devices" criterion, at the service layer: another user's id cannot revoke
// this one's token.
func TestDeviceTokenService_Revoke_ScopedByUser(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	owner := domain.NewUserID()
	_, token := seedActiveToken(f, owner)
	svc := f.service()

	err := svc.Revoke(context.Background(), domain.NewUserID(), token.ID)
	if !errors.Is(err, domain.ErrDeviceTokenNotFound) {
		t.Errorf("Revoke(wrong user) error = %v, want ErrDeviceTokenNotFound", err)
	}
	if !f.tokens.byID[token.ID].Active() {
		t.Error("Revoke(wrong user) must leave the token active")
	}

	if err := svc.Revoke(context.Background(), owner, token.ID); err != nil {
		t.Fatalf("Revoke(owner): %v", err)
	}
	if f.tokens.byID[token.ID].Active() {
		t.Error("Revoke(owner) must revoke the token")
	}
}

// TestDeviceTokenService_RevokeAll_ImplementsCredentialRevoker is the
// automated equivalent of this ticket's cross-ticket note: RevokeAll is
// registered directly into NSTR-21's Revokers slice, satisfying
// app.CredentialRevoker without any adapter-side wrapper.
func TestDeviceTokenService_RevokeAll_ImplementsCredentialRevoker(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	owner := domain.NewUserID()
	_, tokenA := seedActiveToken(f, owner)
	_, tokenB := seedActiveToken(f, owner)
	_, other := seedActiveToken(f, domain.NewUserID())
	svc := f.service()

	var revoker app.CredentialRevoker = svc
	if err := revoker.RevokeAll(context.Background(), owner); err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}
	if f.tokens.byID[tokenA.ID].Active() || f.tokens.byID[tokenB.ID].Active() {
		t.Error("RevokeAll must revoke every one of the target user's tokens")
	}
	if !f.tokens.byID[other.ID].Active() {
		t.Error("RevokeAll must not touch another user's token")
	}
}

func TestDeviceTokenService_RevokeAll_NothingToRevokeIsNotAnError(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	svc := f.service()

	if err := svc.RevokeAll(context.Background(), domain.NewUserID()); err != nil {
		t.Errorf("RevokeAll(no tokens) = %v, want nil", err)
	}
}

func TestDeviceTokenService_RevokeAll_RepositoryErrorWrapped(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	wantErr := errors.New("revoke all boom")
	f.tokens.revokeAllErr = wantErr
	svc := f.service()

	err := svc.RevokeAll(context.Background(), domain.NewUserID())
	if !errors.Is(err, wantErr) {
		t.Errorf("RevokeAll error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestDeviceTokenService_Revoke_RepositoryErrorWrapped(t *testing.T) {
	t.Parallel()
	f := newDeviceTokenFixture()
	wantErr := errors.New("revoke boom")
	f.tokens.revokeErr = wantErr
	svc := f.service()

	err := svc.Revoke(context.Background(), domain.NewUserID(), domain.NewDeviceTokenID())
	if !errors.Is(err, wantErr) {
		t.Errorf("Revoke error = %v, want it to wrap %v", err, wantErr)
	}
}
