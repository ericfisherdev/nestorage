package adapter_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
)

// stubResolver is a configurable adapter.Resolver fake for Chain's own
// precedence tests: it isolates Chain's dispatch logic from the concrete
// session/device-token/api-key resolvers, which have their own tests below.
type stubResolver struct {
	principal domain.Principal
	found     bool
	err       error
	called    bool
}

func (s *stubResolver) Resolve(_ context.Context, _ *http.Request) (domain.Principal, bool, error) {
	s.called = true
	return s.principal, s.found, s.err
}

func newBearerRequest(t *testing.T, secret string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/bins", nil)
	if secret != "" {
		r.Header.Set("Authorization", "Bearer "+secret)
	}
	return r
}

// fakeDeviceTokenAuthenticator is a configurable deviceTokenAuthenticator
// fake for deviceTokenResolver's tests: a non-nil err simulates any
// rejection DeviceTokenService.Authenticate can return (unknown, revoked, or
// owner-deactivated alike) — deviceTokenResolver collapses all of them to
// ErrInvalidCredential, so the tests below only need one error case.
type fakeDeviceTokenAuthenticator struct {
	user *domain.User
	err  error
}

func (f *fakeDeviceTokenAuthenticator) Authenticate(_ context.Context, _ string) (*domain.User, *domain.DeviceToken, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.user, &domain.DeviceToken{}, nil
}

// fakeAPIKeyAuthenticator is a configurable apiKeyAuthenticator fake for
// apiKeyResolver's tests, the same rationale as fakeDeviceTokenAuthenticator.
type fakeAPIKeyAuthenticator struct {
	key *domain.APIKey
	err error
}

func (f *fakeAPIKeyAuthenticator) Authenticate(_ context.Context, _ string) (*domain.APIKey, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.key, nil
}

func TestNewChain_NilDependenciesPanic(t *testing.T) {
	stub := &stubResolver{}
	tests := []struct {
		name                         string
		session, deviceToken, apiKey adapter.Resolver
	}{
		{"nil session", nil, stub, stub},
		{"nil device token", stub, nil, stub},
		{"nil api key", stub, stub, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("NewChain(...) did not panic")
				}
			}()
			adapter.NewChain(tt.session, tt.deviceToken, tt.apiKey)
		})
	}
}

func TestChain_NoAuthorizationHeader_RunsSessionResolver(t *testing.T) {
	want := domain.NewUserPrincipal(domain.NewUserID(), domain.RoleMember, "Daniel")
	sess := &stubResolver{principal: want, found: true}
	deviceToken := &stubResolver{}
	apiKey := &stubResolver{}
	chain := adapter.NewChain(sess, deviceToken, apiKey)

	r := httptest.NewRequest(http.MethodGet, "/bins", nil)
	p, ok, err := chain.Resolve(r.Context(), r)

	if !ok || err != nil || p != want {
		t.Fatalf("Resolve() = (%+v, %v, %v), want (%+v, true, nil)", p, ok, err, want)
	}
	if !sess.called || deviceToken.called || apiKey.called {
		t.Error("only the session resolver should have been called")
	}
}

func TestChain_DeviceTokenBearer_DispatchesToDeviceTokenResolver(t *testing.T) {
	want := domain.NewUserPrincipal(domain.NewUserID(), domain.RoleMember, "Android device")
	sess := &stubResolver{}
	deviceToken := &stubResolver{principal: want, found: true}
	apiKey := &stubResolver{}
	chain := adapter.NewChain(sess, deviceToken, apiKey)

	r := newBearerRequest(t, domain.DeviceTokenPrefix+"aaaa")
	p, ok, err := chain.Resolve(r.Context(), r)

	if !ok || err != nil || p != want {
		t.Fatalf("Resolve() = (%+v, %v, %v), want (%+v, true, nil)", p, ok, err, want)
	}
	if sess.called || !deviceToken.called || apiKey.called {
		t.Error("only the device token resolver should have been called")
	}
}

func TestChain_APIKeyBearer_DispatchesToAPIKeyResolver_BearerBeatsSessionCookie(t *testing.T) {
	want := domain.NewIntegrationPrincipal("Nestova")
	sess := &stubResolver{principal: domain.NewUserPrincipal(domain.NewUserID(), domain.RoleAdmin, "Maya"), found: true}
	deviceToken := &stubResolver{}
	apiKey := &stubResolver{principal: want, found: true}
	chain := adapter.NewChain(sess, deviceToken, apiKey)

	r := newBearerRequest(t, domain.APIKeyPrefix+"aaaa")
	r.AddCookie(&http.Cookie{Name: "session", Value: "stale-cookie"})
	p, ok, err := chain.Resolve(r.Context(), r)

	if !ok || err != nil || p != want {
		t.Fatalf("Resolve() = (%+v, %v, %v), want (%+v, true, nil) — a Bearer credential must win over a session cookie", p, ok, err, want)
	}
	if sess.called {
		t.Error("the session resolver must not run when a Bearer credential is present, even alongside a cookie")
	}
}

func TestChain_UnrecognizedBearerPrefix_RejectsWithoutConsultingAnyResolver(t *testing.T) {
	sess := &stubResolver{}
	deviceToken := &stubResolver{}
	apiKey := &stubResolver{}
	chain := adapter.NewChain(sess, deviceToken, apiKey)

	r := newBearerRequest(t, "xyz_notacredential")
	_, ok, err := chain.Resolve(r.Context(), r)

	if ok || !errors.Is(err, adapter.ErrInvalidCredential) {
		t.Fatalf("Resolve() = (_, %v, %v), want (false, ErrInvalidCredential)", ok, err)
	}
	if sess.called || deviceToken.called || apiKey.called {
		t.Error("an unrecognized prefix must not fall through to any resolver")
	}
}

func TestChain_NonBearerAuthorizationScheme_RejectsWithoutFallingBackToSession(t *testing.T) {
	sess := &stubResolver{}
	chain := adapter.NewChain(sess, &stubResolver{}, &stubResolver{})

	r := httptest.NewRequest(http.MethodGet, "/bins", nil)
	r.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	_, ok, err := chain.Resolve(r.Context(), r)

	if ok || !errors.Is(err, adapter.ErrInvalidCredential) {
		t.Fatalf("Resolve() = (_, %v, %v), want (false, ErrInvalidCredential)", ok, err)
	}
	if sess.called {
		t.Error("a non-Bearer Authorization header must not fall back to the session resolver")
	}
}

func TestChain_ResolverError_ShortCircuitsAndIsNotSwallowed(t *testing.T) {
	wantErr := errors.New("boom")
	deviceToken := &stubResolver{err: wantErr}
	chain := adapter.NewChain(&stubResolver{}, deviceToken, &stubResolver{})

	r := newBearerRequest(t, domain.DeviceTokenPrefix+"aaaa")
	_, ok, err := chain.Resolve(r.Context(), r)

	if ok || !errors.Is(err, wantErr) {
		t.Fatalf("Resolve() = (_, %v, %v), want the resolver's own error surfaced, not swallowed", ok, err)
	}
}

func TestChain_AbsenceOfEveryCredential_YieldsAnonymous(t *testing.T) {
	chain := adapter.NewChain(&stubResolver{}, &stubResolver{}, &stubResolver{})

	r := httptest.NewRequest(http.MethodGet, "/bins", nil)
	p, ok, err := chain.Resolve(r.Context(), r)

	if ok || err != nil || !p.IsAnonymous() {
		t.Fatalf("Resolve() = (%+v, %v, %v), want (zero, false, nil)", p, ok, err)
	}
}

// --- sessionResolver ---

func TestNewSessionResolver_NilDependenciesPanic(t *testing.T) {
	repo := &fakeCurrentUserRepo{}
	t.Run("nil session manager", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewSessionResolver(nil, ...) did not panic")
			}
		}()
		adapter.NewSessionResolver(nil, repo, testLogger())
	})
	t.Run("nil currentUserFinder", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewSessionResolver(..., nil, ...) did not panic")
			}
		}()
		adapter.NewSessionResolver(scs.New(), nil, testLogger())
	})
	t.Run("nil logger", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewSessionResolver(..., nil) did not panic")
			}
		}()
		adapter.NewSessionResolver(scs.New(), repo, nil)
	})
}

func TestSessionResolver_NoSession_IsNotFound(t *testing.T) {
	sm := scs.New()
	resolver := adapter.NewSessionResolver(sm, &fakeCurrentUserRepo{}, testLogger())

	ctx, err := sm.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("sm.Load: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/bins", nil).WithContext(ctx)

	p, ok, err := resolver.Resolve(ctx, r)
	if ok || err != nil || !p.IsAnonymous() {
		t.Errorf("Resolve() = (%+v, %v, %v), want (zero, false, nil)", p, ok, err)
	}
}

func TestSessionResolver_ValidSession_ResolvesUserPrincipal(t *testing.T) {
	userID := domain.NewUserID()
	repo := &fakeCurrentUserRepo{users: map[domain.UserID]*domain.User{
		userID: {ID: userID, DisplayName: "Maya", Role: domain.RoleAdmin, Active: true},
	}}
	sm := scs.New()
	resolver := adapter.NewSessionResolver(sm, repo, testLogger())

	ctx, err := sm.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("sm.Load: %v", err)
	}
	sm.Put(ctx, session.KeyUserID, userID.String())
	r := httptest.NewRequest(http.MethodGet, "/bins", nil).WithContext(ctx)

	p, ok, err := resolver.Resolve(ctx, r)
	if !ok || err != nil {
		t.Fatalf("Resolve() = (%+v, %v, %v), want ok", p, ok, err)
	}
	want := domain.NewUserPrincipal(userID, domain.RoleAdmin, "Maya")
	if p != want {
		t.Errorf("Resolve() principal = %+v, want %+v", p, want)
	}
}

func TestSessionResolver_UnknownOrInactiveUser_IsNotFoundNotError(t *testing.T) {
	inactiveID := domain.NewUserID()
	tests := []struct {
		name string
		repo *fakeCurrentUserRepo
		seed domain.UserID
	}{
		{"unknown user", &fakeCurrentUserRepo{users: map[domain.UserID]*domain.User{}}, domain.NewUserID()},
		{
			"inactive user",
			&fakeCurrentUserRepo{users: map[domain.UserID]*domain.User{inactiveID: {ID: inactiveID, Active: false}}},
			inactiveID,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := scs.New()
			resolver := adapter.NewSessionResolver(sm, tt.repo, testLogger())

			ctx, err := sm.Load(context.Background(), "")
			if err != nil {
				t.Fatalf("sm.Load: %v", err)
			}
			sm.Put(ctx, session.KeyUserID, tt.seed.String())
			r := httptest.NewRequest(http.MethodGet, "/bins", nil).WithContext(ctx)

			p, ok, err := resolver.Resolve(ctx, r)
			if ok || err != nil || !p.IsAnonymous() {
				t.Errorf("Resolve() = (%+v, %v, %v), want (zero, false, nil) — an unrecoverable session key must not surface as an error", p, ok, err)
			}
		})
	}
}

// --- deviceTokenResolver ---

func TestNewDeviceTokenResolver_NilDependencyPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewDeviceTokenResolver(nil) did not panic")
		}
	}()
	adapter.NewDeviceTokenResolver(nil)
}

func TestDeviceTokenResolver_NoBearerCredential_IsNotFound(t *testing.T) {
	resolver := adapter.NewDeviceTokenResolver(&fakeDeviceTokenAuthenticator{})
	r := httptest.NewRequest(http.MethodGet, "/bins", nil)

	p, ok, err := resolver.Resolve(r.Context(), r)
	if ok || err != nil || !p.IsAnonymous() {
		t.Errorf("Resolve() = (%+v, %v, %v), want (zero, false, nil)", p, ok, err)
	}
}

func TestDeviceTokenResolver_WrongPrefix_IsNotFound(t *testing.T) {
	resolver := adapter.NewDeviceTokenResolver(&fakeDeviceTokenAuthenticator{})
	r := newBearerRequest(t, domain.APIKeyPrefix+"aaaa")

	p, ok, err := resolver.Resolve(r.Context(), r)
	if ok || err != nil || !p.IsAnonymous() {
		t.Errorf("Resolve() = (%+v, %v, %v), want (zero, false, nil)", p, ok, err)
	}
}

func TestDeviceTokenResolver_ValidToken_ResolvesUserPrincipal(t *testing.T) {
	userID := domain.NewUserID()
	authn := &fakeDeviceTokenAuthenticator{user: &domain.User{ID: userID, DisplayName: "Android device", Role: domain.RoleMember, Active: true}}
	resolver := adapter.NewDeviceTokenResolver(authn)
	r := newBearerRequest(t, domain.DeviceTokenPrefix+"aaaa")

	p, ok, err := resolver.Resolve(r.Context(), r)
	if !ok || err != nil {
		t.Fatalf("Resolve() = (%+v, %v, %v), want ok", p, ok, err)
	}
	want := domain.NewUserPrincipal(userID, domain.RoleMember, "Android device")
	if p != want {
		t.Errorf("Resolve() principal = %+v, want %+v", p, want)
	}
}

func TestDeviceTokenResolver_InvalidRevokedOrExpiredToken_WrapsErrInvalidCredential(t *testing.T) {
	authn := &fakeDeviceTokenAuthenticator{err: domain.ErrDeviceTokenRevoked}
	resolver := adapter.NewDeviceTokenResolver(authn)
	r := newBearerRequest(t, domain.DeviceTokenPrefix+"aaaa")

	_, ok, err := resolver.Resolve(r.Context(), r)
	if ok || !errors.Is(err, adapter.ErrInvalidCredential) {
		t.Fatalf("Resolve() = (_, %v, %v), want (false, ErrInvalidCredential) — the specific reason must not leak past this wrap", ok, err)
	}
}

// --- apiKeyResolver ---

func TestNewAPIKeyResolver_NilDependencyPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewAPIKeyResolver(nil) did not panic")
		}
	}()
	adapter.NewAPIKeyResolver(nil)
}

func TestAPIKeyResolver_NoBearerCredential_IsNotFound(t *testing.T) {
	resolver := adapter.NewAPIKeyResolver(&fakeAPIKeyAuthenticator{})
	r := httptest.NewRequest(http.MethodGet, "/bins", nil)

	p, ok, err := resolver.Resolve(r.Context(), r)
	if ok || err != nil || !p.IsAnonymous() {
		t.Errorf("Resolve() = (%+v, %v, %v), want (zero, false, nil)", p, ok, err)
	}
}

func TestAPIKeyResolver_WrongPrefix_IsNotFound(t *testing.T) {
	resolver := adapter.NewAPIKeyResolver(&fakeAPIKeyAuthenticator{})
	r := newBearerRequest(t, domain.DeviceTokenPrefix+"aaaa")

	p, ok, err := resolver.Resolve(r.Context(), r)
	if ok || err != nil || !p.IsAnonymous() {
		t.Errorf("Resolve() = (%+v, %v, %v), want (zero, false, nil)", p, ok, err)
	}
}

func TestAPIKeyResolver_ValidKey_ResolvesIntegrationPrincipal(t *testing.T) {
	authn := &fakeAPIKeyAuthenticator{key: &domain.APIKey{Label: "Nestova"}}
	resolver := adapter.NewAPIKeyResolver(authn)
	r := newBearerRequest(t, domain.APIKeyPrefix+"aaaa")

	p, ok, err := resolver.Resolve(r.Context(), r)
	if !ok || err != nil {
		t.Fatalf("Resolve() = (%+v, %v, %v), want ok", p, ok, err)
	}
	want := domain.NewIntegrationPrincipal("Nestova")
	if p != want {
		t.Errorf("Resolve() principal = %+v, want %+v", p, want)
	}
}

func TestAPIKeyResolver_InvalidRevokedOrExpiredKey_WrapsErrInvalidCredential(t *testing.T) {
	authn := &fakeAPIKeyAuthenticator{err: domain.ErrAPIKeyExpired}
	resolver := adapter.NewAPIKeyResolver(authn)
	r := newBearerRequest(t, domain.APIKeyPrefix+"aaaa")

	_, ok, err := resolver.Resolve(r.Context(), r)
	if ok || !errors.Is(err, adapter.ErrInvalidCredential) {
		t.Fatalf("Resolve() = (_, %v, %v), want (false, ErrInvalidCredential) — the specific reason must not leak past this wrap", ok, err)
	}
}

// --- CurrentPrincipal ---

func TestCurrentPrincipal_Absent(t *testing.T) {
	if _, ok := adapter.CurrentPrincipal(context.Background()); ok {
		t.Error("CurrentPrincipal(background) = true, want false")
	}
}
