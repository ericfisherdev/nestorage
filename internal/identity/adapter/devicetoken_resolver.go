package adapter

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// deviceTokenAuthenticator is the narrow port (ISP) deviceTokenResolver
// depends on, satisfied by *app.DeviceTokenService.
type deviceTokenAuthenticator interface {
	Authenticate(ctx context.Context, presented string) (*domain.User, *domain.DeviceToken, error)
}

// deviceTokenResolver wraps NSTR-22's device-token authentication into a
// Resolver. Chain only ever calls this once it has already matched
// domain.DeviceTokenPrefix, but Resolve re-derives the token from the
// request itself rather than trusting a pre-extracted value, so it stays
// correct when exercised directly (e.g. in a unit test).
type deviceTokenResolver struct {
	tokens deviceTokenAuthenticator
}

// NewDeviceTokenResolver constructs the Resolver Chain dispatches
// domain.DeviceTokenPrefix bearer secrets to. tokens is required; a nil
// value panics at construction time.
func NewDeviceTokenResolver(tokens deviceTokenAuthenticator) Resolver {
	if tokens == nil {
		panic("identity/adapter: NewDeviceTokenResolver requires a non-nil deviceTokenAuthenticator")
	}
	return &deviceTokenResolver{tokens: tokens}
}

// Resolve authenticates the request's bearer device token. An unrecognized,
// revoked, or owner-deactivated token all wrap ErrInvalidCredential —
// DeviceTokenService.Authenticate's three-way distinction is deliberately
// collapsed here: Resolver's contract requires a rejected credential to come
// back as an error, never as "not found", so the middleware answers 401
// uniformly regardless of which check failed (see Denier's own doc).
func (dr *deviceTokenResolver) Resolve(ctx context.Context, r *http.Request) (domain.Principal, bool, error) {
	token, ok := bearerToken(r)
	if !ok || !strings.HasPrefix(token, domain.DeviceTokenPrefix) {
		return domain.Principal{}, false, nil
	}
	user, _, err := dr.tokens.Authenticate(ctx, token)
	if err != nil {
		return domain.Principal{}, false, fmt.Errorf("%w: %v", ErrInvalidCredential, err)
	}
	return domain.NewUserPrincipal(user.ID, user.Role, user.DisplayName), true, nil
}
