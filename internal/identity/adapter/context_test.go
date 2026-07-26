package adapter_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

func TestPrincipalKindLabel_NoPrincipal_Anonymous(t *testing.T) {
	if got := adapter.PrincipalKindLabel(context.Background()); got != "anonymous" {
		t.Errorf("PrincipalKindLabel(no principal) = %q, want %q", got, "anonymous")
	}
}

// TestPrincipalKindLabel_UserPrincipal_User and
// TestPrincipalKindLabel_IntegrationPrincipal_Integration route a request
// through the real Resolve middleware (rather than constructing a Principal
// context value directly) so this also proves PrincipalKindLabel reads back
// exactly what Resolve wrote, the same round trip api.Observe relies on in
// production.
func TestPrincipalKindLabel_UserPrincipal_User(t *testing.T) {
	session := &stubResolver{principal: domain.NewUserPrincipal(domain.NewUserID(), domain.RoleMember, "Daniel"), found: true}
	chain := adapter.NewChain(session, &stubResolver{}, &stubResolver{})
	denier := adapter.NewDenier(testLogger())

	var got string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = adapter.PrincipalKindLabel(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	mux := adapter.Resolve(chain, denier, testLogger())(next)
	r := httptest.NewRequest(http.MethodGet, "/bins", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if got != "user" {
		t.Errorf("PrincipalKindLabel(user principal) = %q, want %q", got, "user")
	}
}

func TestPrincipalKindLabel_IntegrationPrincipal_Integration(t *testing.T) {
	apiKey := &stubResolver{principal: domain.NewIntegrationPrincipal("Nestova"), found: true}
	chain := adapter.NewChain(&stubResolver{}, &stubResolver{}, apiKey)
	denier := adapter.NewDenier(testLogger())

	var got string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = adapter.PrincipalKindLabel(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	mux := adapter.Resolve(chain, denier, testLogger())(next)
	r := newBearerRequest(t, domain.APIKeyPrefix+"aaaa")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if got != "integration" {
		t.Errorf("PrincipalKindLabel(integration principal) = %q, want %q", got, "integration")
	}
}
