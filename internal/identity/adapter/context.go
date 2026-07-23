package adapter

import (
	"context"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// principalContextKey is the unexported type for the context key Resolve
// stores the resolved Principal under, matching nestcore's
// httpserver/middleware.contextKey idiom — an unexported int type so it can
// never collide with a key from another package.
type principalContextKey int

// principalKey is the one key ever stored under principalContextKey.
const principalKey principalContextKey = 0

// withPrincipal returns a copy of ctx carrying p, retrievable via
// CurrentPrincipal.
func withPrincipal(ctx context.Context, p domain.Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// CurrentPrincipal returns the Principal Resolve placed in ctx, and false
// when no credential resolved for this request. This is the single accessor
// every handler and service reads back through — no handler reads a
// session, header, or cookie directly once Resolve has run.
func CurrentPrincipal(ctx context.Context) (domain.Principal, bool) {
	p, ok := ctx.Value(principalKey).(domain.Principal)
	return p, ok
}
