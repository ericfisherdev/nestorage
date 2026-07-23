package app_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ericfisherdev/nestorage/internal/identity/app"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// fakeAPIKeyRepo is an in-memory apiKeyRepository fake, mirroring
// fakeDeviceTokenRepo's own shape. hasCurrent reproduces
// api_key_current_uniq's invariant (Create fails when a current row already
// exists) without a real database.
type fakeAPIKeyRepo struct {
	byID   map[domain.APIKeyID]*domain.APIKey
	byHash map[string]*domain.APIKey

	createErr    error
	getByHashErr error
	listErr      error
	rotateErr    error
	revokeErr    error
	touchErr     error

	getByHashCalls int
	touchCalls     []domain.APIKeyID
}

func newFakeAPIKeyRepo() *fakeAPIKeyRepo {
	return &fakeAPIKeyRepo{
		byID:   make(map[domain.APIKeyID]*domain.APIKey),
		byHash: make(map[string]*domain.APIKey),
	}
}

func (f *fakeAPIKeyRepo) hasCurrent() bool {
	for _, k := range f.byID {
		if k.RevokedAt == nil && k.ExpiresAt == nil {
			return true
		}
	}
	return false
}

func (f *fakeAPIKeyRepo) Create(_ context.Context, k *domain.APIKey) error {
	if f.createErr != nil {
		return f.createErr
	}
	if f.hasCurrent() {
		return domain.ErrAPIKeyExists
	}
	k.CreatedAt = fixedClock()
	f.byID[k.ID] = k
	f.byHash[k.SecretHash] = k
	return nil
}

func (f *fakeAPIKeyRepo) GetBySecretHash(_ context.Context, hash string) (*domain.APIKey, error) {
	f.getByHashCalls++
	if f.getByHashErr != nil {
		return nil, f.getByHashErr
	}
	k, ok := f.byHash[hash]
	if !ok {
		return nil, domain.ErrAPIKeyNotFound
	}
	return k, nil
}

func (f *fakeAPIKeyRepo) ListAll(_ context.Context) ([]*domain.APIKey, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]*domain.APIKey, 0, len(f.byID))
	for _, k := range f.byID {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (f *fakeAPIKeyRepo) Rotate(_ context.Context, incumbentID domain.APIKeyID, expiresAt time.Time, next *domain.APIKey) error {
	if f.rotateErr != nil {
		return f.rotateErr
	}
	var zero domain.APIKeyID
	if incumbentID != zero {
		incumbent, ok := f.byID[incumbentID]
		if !ok || incumbent.RevokedAt != nil {
			return domain.ErrAPIKeyNotFound
		}
		supersededAt := expiresAt
		incumbent.ExpiresAt = &supersededAt
	}
	// Ensure the new row sorts after the incumbent, matching ListAll's
	// newest-first ordering on a real INSERT that happens after the
	// supersede UPDATE within the same transaction.
	next.CreatedAt = fixedClock().Add(time.Second)
	f.byID[next.ID] = next
	f.byHash[next.SecretHash] = next
	return nil
}

func (f *fakeAPIKeyRepo) Revoke(_ context.Context, id domain.APIKeyID, revokedAt time.Time) error {
	if f.revokeErr != nil {
		return f.revokeErr
	}
	k, ok := f.byID[id]
	if !ok || k.RevokedAt != nil {
		return domain.ErrAPIKeyNotFound
	}
	k.RevokedAt = &revokedAt
	return nil
}

func (f *fakeAPIKeyRepo) TouchLastUsed(_ context.Context, id domain.APIKeyID, now, _ time.Time) error {
	f.touchCalls = append(f.touchCalls, id)
	if f.touchErr != nil {
		return f.touchErr
	}
	if k, ok := f.byID[id]; ok {
		k.LastUsedAt = &now
	}
	return nil
}

// apiKeyFixture bundles the fake repository APIKeyService needs, so each
// test only overrides the field it cares about.
type apiKeyFixture struct {
	keys *fakeAPIKeyRepo
}

func newAPIKeyFixture() *apiKeyFixture {
	return &apiKeyFixture{keys: newFakeAPIKeyRepo()}
}

func (f *apiKeyFixture) service(t *testing.T) *app.APIKeyService {
	t.Helper()
	svc, err := app.NewAPIKeyService(f.keys, fixedClock, testLogger())
	if err != nil {
		t.Fatalf("NewAPIKeyService: %v", err)
	}
	return svc
}

// seedCurrentKey inserts a current (unsuperseded, unrevoked) key directly
// into the fake store, bypassing Create, so a test can set up "a key
// already exists" without depending on Create's own behavior.
func seedCurrentKey(f *apiKeyFixture, label string) *domain.APIKey {
	k := &domain.APIKey{
		ID:         domain.NewAPIKeyID(),
		KeyPrefix:  "ns_deadbeef",
		SecretHash: "seed-hash-" + label,
		Label:      label,
		CreatedAt:  fixedClock(),
	}
	f.keys.byID[k.ID] = k
	f.keys.byHash[k.SecretHash] = k
	return k
}

func TestNewAPIKeyService_NilDependenciesReturnError(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	tests := []struct {
		name string
		fn   func() error
	}{
		{"nil repository", func() error {
			_, err := app.NewAPIKeyService(nil, fixedClock, testLogger())
			return err
		}},
		{"nil clock", func() error {
			_, err := app.NewAPIKeyService(f.keys, nil, testLogger())
			return err
		}},
		{"nil logger", func() error {
			_, err := app.NewAPIKeyService(f.keys, fixedClock, nil)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := tt.fn(); err == nil {
				t.Errorf("NewAPIKeyService(%s) = nil error, want a non-nil error (not a panic)", tt.name)
			}
		})
	}
}

func TestAPIKeyService_Create_ReturnsPlaintextOnceAndStoresOnlyHash(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	svc := f.service(t)

	key, plaintext, err := svc.Create(context.Background(), "Nestova integration")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if plaintext == "" {
		t.Fatal("Create returned an empty plaintext")
	}
	if !strings.HasPrefix(plaintext, domain.APIKeyPrefix) {
		t.Errorf("plaintext = %q, want prefix %q", plaintext, domain.APIKeyPrefix)
	}
	if key.SecretHash == plaintext {
		t.Error("the persisted SecretHash equals the returned plaintext — the raw secret must never be stored")
	}
	if key.SecretHash != domain.HashAPIKeySecret(plaintext) {
		t.Error("the persisted SecretHash does not match HashAPIKeySecret(plaintext)")
	}
	if key.KeyPrefix != domain.KeyPrefixOf(plaintext) {
		t.Errorf("key.KeyPrefix = %q, want %q", key.KeyPrefix, domain.KeyPrefixOf(plaintext))
	}
	stored, ok := f.keys.byID[key.ID]
	if !ok {
		t.Fatal("Create did not persist the key via the repository")
	}
	if stored.SecretHash == plaintext {
		t.Error("the repository's stored copy holds the plaintext instead of its hash")
	}
}

func TestAPIKeyService_Create_TrimsLabel(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	svc := f.service(t)

	key, _, err := svc.Create(context.Background(), "  Nestova integration  ")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if key.Label != "Nestova integration" {
		t.Errorf("key.Label = %q, want trimmed %q", key.Label, "Nestova integration")
	}
}

func TestAPIKeyService_Create_RejectsBlankLabel(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	svc := f.service(t)

	_, _, err := svc.Create(context.Background(), "   ")
	if !errors.Is(err, domain.ErrInvalidAPIKey) {
		t.Errorf("Create(blank label) error = %v, want wrapped ErrInvalidAPIKey", err)
	}
	if len(f.keys.byID) != 0 {
		t.Error("Create must reject a blank label before ever touching the repository")
	}
}

func TestAPIKeyService_Create_RejectsOverLongLabel(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	svc := f.service(t)

	_, _, err := svc.Create(context.Background(), strings.Repeat("a", 101))
	if !errors.Is(err, domain.ErrInvalidAPIKey) {
		t.Errorf("Create(101-char label) error = %v, want wrapped ErrInvalidAPIKey", err)
	}
}

// TestAPIKeyService_Create_AlreadyExists is the automated equivalent of this
// ticket's "at most one current key can exist" criterion, at the service
// layer: Create is unambiguous and refuses to replace an existing key.
func TestAPIKeyService_Create_AlreadyExists(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	seedCurrentKey(f, "existing")
	svc := f.service(t)

	_, _, err := svc.Create(context.Background(), "new key")
	if !errors.Is(err, domain.ErrAPIKeyExists) {
		t.Errorf("Create(current key exists) error = %v, want ErrAPIKeyExists", err)
	}
}

func TestAPIKeyService_Create_RepositoryErrorWrapped(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	wantErr := errors.New("create boom")
	f.keys.createErr = wantErr
	svc := f.service(t)

	_, _, err := svc.Create(context.Background(), "Nestova integration")
	if !errors.Is(err, wantErr) {
		t.Errorf("Create error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestAPIKeyService_Current_NoneYet(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	svc := f.service(t)

	key, found, err := svc.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if found || key != nil {
		t.Errorf("Current() = (%v, %v), want (nil, false)", key, found)
	}
}

// TestAPIKeyService_Current_ExcludesRetiring is the automated equivalent of
// this ticket's rotation-window criterion: during the overlap window, the
// superseded key must not be reported as the current one.
func TestAPIKeyService_Current_ExcludesRetiring(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	retiring := seedCurrentKey(f, "retiring")
	future := fixedClock().Add(time.Hour)
	retiring.ExpiresAt = &future
	current := &domain.APIKey{ID: domain.NewAPIKeyID(), KeyPrefix: "ns_11111111", SecretHash: "current-hash", Label: "current", CreatedAt: fixedClock().Add(time.Second)}
	f.keys.byID[current.ID] = current
	f.keys.byHash[current.SecretHash] = current
	svc := f.service(t)

	got, found, err := svc.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if !found {
		t.Fatal("Current() found = false, want true")
	}
	if got.ID != current.ID {
		t.Errorf("Current() = %v, want the unsuperseded key %v", got.ID, current.ID)
	}
}

func TestAPIKeyService_Current_RepositoryErrorWrapped(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	wantErr := errors.New("list boom")
	f.keys.listErr = wantErr
	svc := f.service(t)

	_, _, err := svc.Current(context.Background())
	if !errors.Is(err, wantErr) {
		t.Errorf("Current error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestAPIKeyService_List_ReturnsAll(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	seedCurrentKey(f, "a")
	svc := f.service(t)

	got, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("List returned %d keys, want 1", len(got))
	}
}

func TestAPIKeyService_List_RepositoryErrorWrapped(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	wantErr := errors.New("list boom")
	f.keys.listErr = wantErr
	svc := f.service(t)

	_, err := svc.List(context.Background())
	if !errors.Is(err, wantErr) {
		t.Errorf("List error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestAPIKeyService_Rotate_NoIncumbent_CreatesFirstKey(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	svc := f.service(t)

	key, plaintext, err := svc.Rotate(context.Background(), "Nestova integration", domain.OverlapNone)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if plaintext == "" {
		t.Fatal("Rotate returned an empty plaintext")
	}
	if key.ExpiresAt != nil {
		t.Error("the first-ever key must not be created with ExpiresAt already set")
	}
}

// TestAPIKeyService_Rotate_SupersedesIncumbentWithinOverlap is the
// automated equivalent of this ticket's "rotation keeps the previous key
// working until the end of the chosen overlap window" criterion.
func TestAPIKeyService_Rotate_SupersedesIncumbentWithinOverlap(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	incumbent := seedCurrentKey(f, "old")
	svc := f.service(t)

	_, _, err := svc.Rotate(context.Background(), "new", domain.Overlap24h)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	got := f.keys.byID[incumbent.ID]
	if got.ExpiresAt == nil {
		t.Fatal("Rotate must set the incumbent's ExpiresAt")
	}
	wantExpiry := fixedClock().Add(24 * time.Hour)
	if !got.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("incumbent ExpiresAt = %v, want %v", got.ExpiresAt, wantExpiry)
	}
	if !got.Usable(fixedClock().Add(23 * time.Hour)) {
		t.Error("the superseded key must stay usable inside its overlap window")
	}
	if got.Usable(fixedClock().Add(25 * time.Hour)) {
		t.Error("the superseded key must stop being usable once its overlap window passes")
	}
}

// TestAPIKeyService_Rotate_NoOverlap_InvalidatesImmediately is the automated
// equivalent of this ticket's "choosing no overlap invalidates it at once"
// criterion.
func TestAPIKeyService_Rotate_NoOverlap_InvalidatesImmediately(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	incumbent := seedCurrentKey(f, "old")
	svc := f.service(t)

	_, _, err := svc.Rotate(context.Background(), "new", domain.OverlapNone)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	got := f.keys.byID[incumbent.ID]
	if got.Usable(fixedClock()) {
		t.Error("OverlapNone must invalidate the incumbent at the rotation instant")
	}
}

func TestAPIKeyService_Rotate_RejectsBlankLabel(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	svc := f.service(t)

	_, _, err := svc.Rotate(context.Background(), "  ", domain.OverlapNone)
	if !errors.Is(err, domain.ErrInvalidAPIKey) {
		t.Errorf("Rotate(blank label) error = %v, want wrapped ErrInvalidAPIKey", err)
	}
}

func TestAPIKeyService_Rotate_RejectsInvalidOverlap(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	svc := f.service(t)

	_, _, err := svc.Rotate(context.Background(), "label", domain.OverlapWindow("30d"))
	if !errors.Is(err, domain.ErrInvalidOverlapWindow) {
		t.Errorf("Rotate(invalid overlap) error = %v, want wrapped ErrInvalidOverlapWindow", err)
	}
}

func TestAPIKeyService_Rotate_CurrentLookupErrorWrapped(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	wantErr := errors.New("current lookup boom")
	f.keys.listErr = wantErr
	svc := f.service(t)

	_, _, err := svc.Rotate(context.Background(), "label", domain.OverlapNone)
	if !errors.Is(err, wantErr) {
		t.Errorf("Rotate error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestAPIKeyService_Rotate_RepositoryErrorWrapped(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	wantErr := errors.New("rotate boom")
	f.keys.rotateErr = wantErr
	svc := f.service(t)

	_, _, err := svc.Rotate(context.Background(), "label", domain.OverlapNone)
	if !errors.Is(err, wantErr) {
		t.Errorf("Rotate error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestAPIKeyService_Revoke_Success(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	current := seedCurrentKey(f, "current")
	svc := f.service(t)

	if err := svc.Revoke(context.Background(), current.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if f.keys.byID[current.ID].RevokedAt == nil {
		t.Error("Revoke must set RevokedAt")
	}
}

func TestAPIKeyService_Revoke_RepositoryErrorWrapped(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	wantErr := errors.New("revoke boom")
	f.keys.revokeErr = wantErr
	svc := f.service(t)

	err := svc.Revoke(context.Background(), domain.NewAPIKeyID())
	if !errors.Is(err, wantErr) {
		t.Errorf("Revoke error = %v, want it to wrap %v", err, wantErr)
	}
}

func seedAuthenticatableKey(f *apiKeyFixture) (raw string, key *domain.APIKey) {
	raw, _ = domain.GenerateAPIKeySecret()
	key = &domain.APIKey{
		ID:         domain.NewAPIKeyID(),
		KeyPrefix:  domain.KeyPrefixOf(raw),
		SecretHash: domain.HashAPIKeySecret(raw),
		Label:      "Nestova integration",
		CreatedAt:  fixedClock(),
	}
	f.keys.byID[key.ID] = key
	f.keys.byHash[key.SecretHash] = key
	return raw, key
}

func TestAPIKeyService_Authenticate_Success(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	raw, key := seedAuthenticatableKey(f)
	svc := f.service(t)

	got, err := svc.Authenticate(context.Background(), raw)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.ID != key.ID {
		t.Errorf("Authenticate() id = %v, want %v", got.ID, key.ID)
	}
	if len(f.keys.touchCalls) != 1 || f.keys.touchCalls[0] != key.ID {
		t.Errorf("touch calls = %v, want exactly one call for %v", f.keys.touchCalls, key.ID)
	}
}

func TestAPIKeyService_Authenticate_UnknownSecret(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	svc := f.service(t)

	_, err := svc.Authenticate(context.Background(), domain.APIKeyPrefix+strings.Repeat("0", 64))
	if !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Errorf("Authenticate(unknown) error = %v, want ErrAPIKeyNotFound", err)
	}
}

// TestAPIKeyService_Authenticate_EmptyPresented_NoRepositoryCall is the
// automated equivalent of the ticket's "reject empty as not-found without a
// database round trip" requirement.
func TestAPIKeyService_Authenticate_EmptyPresented_NoRepositoryCall(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	svc := f.service(t)

	_, err := svc.Authenticate(context.Background(), "   ")
	if !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Errorf("Authenticate(blank) error = %v, want ErrAPIKeyNotFound", err)
	}
	if f.keys.getByHashCalls != 0 {
		t.Error("Authenticate must reject a blank secret before ever touching the repository")
	}
}

// TestAPIKeyService_Authenticate_WrongPrefix_NoRepositoryCall guards the
// NSTR-23 reconciliation's ns_/nsd_ distinguishability requirement: a
// presented secret carrying NSTR-22's device-token prefix must never even
// reach a hash lookup here.
func TestAPIKeyService_Authenticate_WrongPrefix_NoRepositoryCall(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	svc := f.service(t)

	_, err := svc.Authenticate(context.Background(), domain.DeviceTokenPrefix+strings.Repeat("0", 64))
	if !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Errorf("Authenticate(device token prefix) error = %v, want ErrAPIKeyNotFound", err)
	}
	if f.keys.getByHashCalls != 0 {
		t.Error("Authenticate must reject a mismatched prefix before ever touching the repository")
	}
}

// TestAPIKeyService_Authenticate_Revoked is the automated equivalent of this
// ticket's "a revoked key is rejected immediately" criterion.
func TestAPIKeyService_Authenticate_Revoked(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	raw, key := seedAuthenticatableKey(f)
	revokedAt := fixedClock()
	key.RevokedAt = &revokedAt
	svc := f.service(t)

	_, err := svc.Authenticate(context.Background(), raw)
	if !errors.Is(err, domain.ErrAPIKeyRevoked) {
		t.Errorf("Authenticate(revoked) error = %v, want ErrAPIKeyRevoked", err)
	}
	if len(f.keys.touchCalls) != 0 {
		t.Error("Authenticate must not touch last-used for a revoked key")
	}
}

func TestAPIKeyService_Authenticate_Expired(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	raw, key := seedAuthenticatableKey(f)
	past := fixedClock().Add(-time.Hour)
	key.ExpiresAt = &past
	svc := f.service(t)

	_, err := svc.Authenticate(context.Background(), raw)
	if !errors.Is(err, domain.ErrAPIKeyExpired) {
		t.Errorf("Authenticate(expired) error = %v, want ErrAPIKeyExpired", err)
	}
	if len(f.keys.touchCalls) != 0 {
		t.Error("Authenticate must not touch last-used for an expired key")
	}
}

func TestAPIKeyService_Authenticate_RepositoryErrorWrapped(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	wantErr := errors.New("lookup boom")
	f.keys.getByHashErr = wantErr
	svc := f.service(t)

	_, err := svc.Authenticate(context.Background(), domain.APIKeyPrefix+strings.Repeat("0", 64))
	if !errors.Is(err, wantErr) {
		t.Errorf("Authenticate error = %v, want it to wrap %v", err, wantErr)
	}
}

// TestAPIKeyService_Authenticate_TouchFailureIsSwallowed asserts a failed
// best-effort touch never fails an otherwise valid authentication.
func TestAPIKeyService_Authenticate_TouchFailureIsSwallowed(t *testing.T) {
	t.Parallel()
	f := newAPIKeyFixture()
	raw, _ := seedAuthenticatableKey(f)
	f.keys.touchErr = errors.New("touch boom")
	svc := f.service(t)

	if _, err := svc.Authenticate(context.Background(), raw); err != nil {
		t.Errorf("Authenticate = %v, want nil (a touch failure must be swallowed)", err)
	}
}
