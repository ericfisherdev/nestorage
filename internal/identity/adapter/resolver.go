package adapter

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// bearerPrefix is the HTTP Authorization scheme every bearer credential in
// this context (NSTR-22's device tokens, NSTR-23's account api key) uses.
const bearerPrefix = "Bearer "

// ErrInvalidCredential is the sentinel every Resolver returns when the
// request DOES carry one of this context's credentials but it is invalid,
// revoked, expired, or belongs to a deactivated user. Resolve (middleware.go)
// answers 401 for it uniformly through Denier, so the response never reveals
// which of the three checks failed.
var ErrInvalidCredential = errors.New("identity/adapter: invalid credential")

// Resolver resolves a request's credential into a domain.Principal. Every
// credential kind NSTR-24 accepts — the session cookie (NSTR-20), a device
// token (NSTR-22), or the account api key (NSTR-23) — implements this same
// port, wrapping its own domain type into a Principal.
//
// Implementations must observe a three-way contract:
//   - (principal, true, nil) on success.
//   - (zero, false, nil) when THIS resolver's credential is simply absent
//     from the request — not an error, just "try the next stage".
//   - (zero, false, err), err wrapping ErrInvalidCredential, when the
//     credential IS present but rejected.
//
// The false-vs-error distinction is load-bearing: an absent credential falls
// through (to the session fallback, or to anonymous), but an invalid one
// must never be silently downgraded into an anonymous request.
type Resolver interface {
	Resolve(ctx context.Context, r *http.Request) (domain.Principal, bool, error)
}

// bearerToken extracts the raw secret from r's Authorization header when it
// uses the Bearer scheme. ok is false whenever it does not — including when
// the header is absent entirely.
func bearerToken(r *http.Request) (token string, ok bool) {
	return strings.CutPrefix(r.Header.Get("Authorization"), bearerPrefix)
}

// Chain composes the three credential resolvers with the precedence rule
// NSTR-24 owns:
//   - With no Authorization header, the session resolver runs.
//   - A Bearer credential, when present, is dispatched by its prefix to
//     exactly one resolver — domain.APIKeyPrefix to the account api key,
//     domain.DeviceTokenPrefix to the device token — and the session
//     cookie is ignored even when one is also present: a bearer credential
//     is an explicit statement of intent and must not be silently upgraded
//     by a stale cookie.
//   - An Authorization header present but naming neither prefix (including
//     a non-Bearer scheme) is ErrInvalidCredential, not a fall-through to
//     the session resolver.
type Chain struct {
	session     Resolver
	deviceToken Resolver
	apiKey      Resolver
}

// NewChain constructs Chain. All three resolvers are required; a missing one
// panics at construction time, matching every other constructor in this
// package.
func NewChain(session, deviceToken, apiKey Resolver) *Chain {
	if session == nil {
		panic("identity/adapter: NewChain requires a non-nil session Resolver")
	}
	if deviceToken == nil {
		panic("identity/adapter: NewChain requires a non-nil device token Resolver")
	}
	if apiKey == nil {
		panic("identity/adapter: NewChain requires a non-nil api key Resolver")
	}
	return &Chain{session: session, deviceToken: deviceToken, apiKey: apiKey}
}

// Resolve implements the precedence rule described in Chain's own doc.
func (c *Chain) Resolve(ctx context.Context, r *http.Request) (domain.Principal, bool, error) {
	if r.Header.Get("Authorization") == "" {
		return c.session.Resolve(ctx, r)
	}
	token, ok := bearerToken(r)
	if !ok {
		return domain.Principal{}, false, ErrInvalidCredential
	}
	return c.resolveBearer(ctx, r, token)
}

// resolveBearer dispatches token to the resolver its prefix names. An
// unrecognized prefix rejects outright rather than falling through to either
// bearer resolver or the session cookie.
func (c *Chain) resolveBearer(ctx context.Context, r *http.Request, token string) (domain.Principal, bool, error) {
	switch {
	case strings.HasPrefix(token, domain.APIKeyPrefix):
		return c.apiKey.Resolve(ctx, r)
	case strings.HasPrefix(token, domain.DeviceTokenPrefix):
		return c.deviceToken.Resolve(ctx, r)
	default:
		return domain.Principal{}, false, ErrInvalidCredential
	}
}
