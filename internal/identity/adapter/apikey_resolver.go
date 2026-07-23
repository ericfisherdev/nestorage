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

// apiKeyResolver wraps NSTR-23's account api key authentication into a
// Resolver, producing an integration Principal — never a user one: the
// account api key has no household member behind it (see
// domain.NewIntegrationPrincipal's own doc).
type apiKeyResolver struct {
	keys apiKeyAuthenticator
}

// NewAPIKeyResolver constructs the Resolver Chain dispatches
// domain.APIKeyPrefix bearer secrets to. keys is required; a nil value
// panics at construction time.
func NewAPIKeyResolver(keys apiKeyAuthenticator) Resolver {
	if keys == nil {
		panic("identity/adapter: NewAPIKeyResolver requires a non-nil apiKeyAuthenticator")
	}
	return &apiKeyResolver{keys: keys}
}

// Resolve authenticates the request's bearer account api key. An
// unrecognized, revoked, or expired key all wrap ErrInvalidCredential —
// APIKeyService.Authenticate's three-way distinction is deliberately
// collapsed here, the same rationale as deviceTokenResolver.Resolve.
func (ar *apiKeyResolver) Resolve(ctx context.Context, r *http.Request) (domain.Principal, bool, error) {
	token, ok := bearerToken(r)
	if !ok || !strings.HasPrefix(token, domain.APIKeyPrefix) {
		return domain.Principal{}, false, nil
	}
	key, err := ar.keys.Authenticate(ctx, token)
	if err != nil {
		return domain.Principal{}, false, fmt.Errorf("%w: %v", ErrInvalidCredential, err)
	}
	return domain.NewIntegrationPrincipal(key.Label), true, nil
}
