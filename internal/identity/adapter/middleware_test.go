package adapter_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// calledFlagHandler reports (via the returned *bool) whether it ran, so a
// test can prove a denying middleware never invoked next.
func calledFlagHandler() (http.Handler, *bool) {
	called := false
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}), &called
}

func TestResolve_NilDependenciesPanic(t *testing.T) {
	chain := adapter.NewChain(&stubResolver{}, &stubResolver{}, &stubResolver{})
	denier := adapter.NewDenier(testLogger())

	t.Run("nil chain", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("Resolve(nil, ...) did not panic")
			}
		}()
		adapter.Resolve(nil, denier, testLogger())
	})
	t.Run("nil denier", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("Resolve(..., nil, ...) did not panic")
			}
		}()
		adapter.Resolve(chain, nil, testLogger())
	})
	t.Run("nil logger", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("Resolve(..., nil) did not panic")
			}
		}()
		adapter.Resolve(chain, denier, nil)
	})
}

func TestResolve_Anonymous_PassesThroughWithNoPrincipal(t *testing.T) {
	chain := adapter.NewChain(&stubResolver{}, &stubResolver{}, &stubResolver{})
	denier := adapter.NewDenier(testLogger())
	next, called := calledFlagHandler()

	mux := adapter.Resolve(chain, denier, testLogger())(next)
	r := httptest.NewRequest(http.MethodGet, "/bins", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if !*called {
		t.Error("Resolve must let an anonymous request through to next")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestResolve_ValidCredential_StoresPrincipalForNextToRead(t *testing.T) {
	want := domain.NewUserPrincipal(domain.NewUserID(), domain.RoleAdmin, "Maya")
	session := &stubResolver{principal: want, found: true}
	chain := adapter.NewChain(session, &stubResolver{}, &stubResolver{})
	denier := adapter.NewDenier(testLogger())

	var got domain.Principal
	var ok bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = adapter.CurrentPrincipal(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	mux := adapter.Resolve(chain, denier, testLogger())(next)
	r := httptest.NewRequest(http.MethodGet, "/bins", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if !ok || got != want {
		t.Errorf("CurrentPrincipal in next = (%+v, %v), want (%+v, true)", got, ok, want)
	}
}

func TestResolve_InvalidCredential_Returns401AndNeverCallsNext(t *testing.T) {
	deviceToken := &stubResolver{err: fmt.Errorf("device token: %w", adapter.ErrInvalidCredential)}
	chain := adapter.NewChain(&stubResolver{}, deviceToken, &stubResolver{})
	denier := adapter.NewDenier(testLogger())
	next, called := calledFlagHandler()

	mux := adapter.Resolve(chain, denier, testLogger())(next)
	r := newBearerRequest(t, domain.DeviceTokenPrefix+"aaaa")
	r.Header.Set("HX-Request", "true") // forces a literal 401, not a login redirect.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if *called {
		t.Error("Resolve must not call next for an invalid credential")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestResolve_UnexpectedChainError_Returns500AndNeverCallsNext(t *testing.T) {
	// A stub resolver returning an error that does NOT wrap
	// ErrInvalidCredential simulates a genuine infrastructure failure (e.g. a
	// database outage resolving the session), which Resolve must not treat
	// as a rejected credential.
	session := &stubResolver{err: errors.New("database is down")}
	chain := adapter.NewChain(session, &stubResolver{}, &stubResolver{})
	denier := adapter.NewDenier(testLogger())
	next, called := calledFlagHandler()

	mux := adapter.Resolve(chain, denier, testLogger())(next)
	r := httptest.NewRequest(http.MethodGet, "/bins", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if *called {
		t.Error("Resolve must not call next after an unexpected error")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestRequireAuthenticated_NilDenierPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("RequireAuthenticated(nil) did not panic")
		}
	}()
	adapter.RequireAuthenticated(nil)
}

func TestRequireAuthenticated_Anonymous_RedirectsToLogin(t *testing.T) {
	denier := adapter.NewDenier(testLogger())
	next, called := calledFlagHandler()

	mux := adapter.RequireAuthenticated(denier)(next)
	r := httptest.NewRequest(http.MethodGet, "/bins", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if *called {
		t.Error("RequireAuthenticated must not call next for an anonymous request")
	}
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (full navigation redirects to /login)", rec.Code, http.StatusSeeOther)
	}
}

func TestRequireAuthenticated_Authenticated_PassesThrough(t *testing.T) {
	session := &stubResolver{principal: domain.NewUserPrincipal(domain.NewUserID(), domain.RoleMember, "Daniel"), found: true}
	chain := adapter.NewChain(session, &stubResolver{}, &stubResolver{})
	denier := adapter.NewDenier(testLogger())
	next, called := calledFlagHandler()

	mux := adapter.Resolve(chain, denier, testLogger())(adapter.RequireAuthenticated(denier)(next))
	r := httptest.NewRequest(http.MethodGet, "/bins", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if !*called {
		t.Error("RequireAuthenticated must call next once Resolve stored a principal")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestRequireAdmin_NilDenierPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("RequireAdmin(nil) did not panic")
		}
	}()
	adapter.RequireAdmin(nil)
}

func TestRequireAdmin_Anonymous_Returns401(t *testing.T) {
	denier := adapter.NewDenier(testLogger())
	next, called := calledFlagHandler()

	mux := adapter.RequireAdmin(denier)(next)
	r := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	r.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if *called {
		t.Error("RequireAdmin must not call next for an anonymous request")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestRequireAdmin_Member_Returns403(t *testing.T) {
	session := &stubResolver{principal: domain.NewUserPrincipal(domain.NewUserID(), domain.RoleMember, "Daniel"), found: true}
	chain := adapter.NewChain(session, &stubResolver{}, &stubResolver{})
	denier := adapter.NewDenier(testLogger())
	next, called := calledFlagHandler()

	mux := adapter.Resolve(chain, denier, testLogger())(adapter.RequireAdmin(denier)(next))
	r := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if *called {
		t.Error("RequireAdmin must not call next for a non-admin member")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestRequireAdmin_IntegrationPrincipal_Returns403(t *testing.T) {
	// The account api key resolves to an integration principal, which is
	// never admin-equivalent — see domain.NewIntegrationPrincipal's own doc.
	apiKey := &stubResolver{principal: domain.NewIntegrationPrincipal("Nestova"), found: true}
	chain := adapter.NewChain(&stubResolver{}, &stubResolver{}, apiKey)
	denier := adapter.NewDenier(testLogger())
	next, called := calledFlagHandler()

	mux := adapter.Resolve(chain, denier, testLogger())(adapter.RequireAdmin(denier)(next))
	r := newBearerRequest(t, domain.APIKeyPrefix+"aaaa")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if *called {
		t.Error("RequireAdmin must not call next for an integration principal")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestRequireAdmin_Admin_PassesThrough(t *testing.T) {
	session := &stubResolver{principal: domain.NewUserPrincipal(domain.NewUserID(), domain.RoleAdmin, "Maya"), found: true}
	chain := adapter.NewChain(session, &stubResolver{}, &stubResolver{})
	denier := adapter.NewDenier(testLogger())
	next, called := calledFlagHandler()

	mux := adapter.Resolve(chain, denier, testLogger())(adapter.RequireAdmin(denier)(next))
	r := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if !*called {
		t.Error("RequireAdmin must call next for an admin")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}
