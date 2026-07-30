package adapter

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// apiKeyAuthenticator is the narrow port (ISP) apiKeyResolver depends on,
// satisfied by *app.APIKeyService.
type apiKeyAuthenticator interface {
	Authenticate(ctx context.Context, presented string) (*domain.APIKey, error)
}

// householdLister is the narrow port (ISP) apiKeyResolver depends on to
// attach a household to the integration principal it produces: the account
// api key has no household member behind it (see
// domain.NewIntegrationPrincipal's own doc), so its Principal.HouseholdID is
// resolved the same way first-run provisioning resolves one — adopt the
// single existing household. domain.HouseholdRepository is a superset that
// satisfies this.
type householdLister interface {
	List(ctx context.Context) ([]domain.Household, error)
}

// apiKeyResolver wraps NSTR-23's account api key authentication into a
// Resolver, producing an integration Principal — never a user one.
type apiKeyResolver struct {
	keys       apiKeyAuthenticator
	households householdLister
}

// NewAPIKeyResolver constructs the Resolver Chain dispatches
// domain.APIKeyPrefix bearer secrets to. Both dependencies are required; a
// nil value panics at construction time.
func NewAPIKeyResolver(keys apiKeyAuthenticator, households householdLister) Resolver {
	if keys == nil {
		panic("identity/adapter: NewAPIKeyResolver requires a non-nil apiKeyAuthenticator")
	}
	if households == nil {
		panic("identity/adapter: NewAPIKeyResolver requires a non-nil householdLister")
	}
	return &apiKeyResolver{keys: keys, households: households}
}

// Resolve authenticates the request's bearer account api key. An
// unrecognized, revoked, or expired key all wrap ErrInvalidCredential —
// APIKeyService.Authenticate's three-way distinction is deliberately
// collapsed here, the same rationale as deviceTokenResolver.Resolve. Once
// authenticated, the returned Principal's HouseholdID is resolved via
// NSTR-116's adopt-the-single-existing-household rule (household.go's
// ResolveExistingHousehold) rather than left zero: a failure to resolve it
// (no household, or more than one) is itself treated as an invalid
// credential — the integration key cannot be used until the invariant it
// depends on holds.
func (ar *apiKeyResolver) Resolve(ctx context.Context, r *http.Request) (domain.Principal, bool, error) {
	token, ok := bearerToken(r)
	if !ok || !strings.HasPrefix(token, domain.APIKeyPrefix) {
		return domain.Principal{}, false, nil
	}
	key, err := ar.keys.Authenticate(ctx, token)
	if err != nil {
		return domain.Principal{}, false, fmt.Errorf("%w: %v", ErrInvalidCredential, err)
	}
	households, err := ar.households.List(ctx)
	if err != nil {
		return domain.Principal{}, false, fmt.Errorf("%w: %v", ErrInvalidCredential, err)
	}
	householdID, err := domain.ResolveExistingHousehold(households)
	if err != nil {
		return domain.Principal{}, false, fmt.Errorf("%w: %v", ErrInvalidCredential, err)
	}
	p := domain.NewIntegrationPrincipal(key.Label)
	p.HouseholdID = householdID
	return p, true, nil
}
