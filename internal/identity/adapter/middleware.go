package adapter

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/ericfisherdev/nestcore/httpserver/middleware"
)

// Resolve is the one middleware that turns any of this context's three
// credentials into a domain.Principal, stored in the request context for
// CurrentPrincipal to read back. It never rejects a request outright on an
// ABSENT credential — the request proceeds anonymously so public routes
// (login, setup, health checks, static assets) keep working; RequireAuthenticated
// and RequireAdmin enforce for the routes that need a principal. An actually
// INVALID credential (chain.Resolve wrapping ErrInvalidCredential) is denied
// with 401 immediately, before next ever runs — the one case Resolve itself
// rejects. Any other error is logged and answered with a generic 500.
//
// Panics on a nil chain, denier, or logger, matching every other constructor
// in this package.
func Resolve(chain *Chain, denier *Denier, logger *slog.Logger) middleware.Middleware {
	if chain == nil {
		panic("identity/adapter: Resolve requires a non-nil Chain")
	}
	if denier == nil {
		panic("identity/adapter: Resolve requires a non-nil Denier")
	}
	if logger == nil {
		panic("identity/adapter: Resolve requires a non-nil logger")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok, err := chain.Resolve(r.Context(), r)
			switch {
			case errors.Is(err, ErrInvalidCredential):
				denier.Deny(w, r, http.StatusUnauthorized)
				return
			case err != nil:
				logger.ErrorContext(r.Context(), "resolve: chain", "error", err)
				http.Error(w, errInternalServerError, http.StatusInternalServerError)
				return
			case ok:
				r = r.WithContext(withPrincipal(r.Context(), p))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuthenticated enforces that a Principal is present in the request
// context (injected by Resolve): denied via Denier when the request resolved
// anonymous.
func RequireAuthenticated(denier *Denier) middleware.Middleware {
	if denier == nil {
		panic("identity/adapter: RequireAuthenticated requires a non-nil Denier")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := CurrentPrincipal(r.Context()); ok {
				next.ServeHTTP(w, r)
				return
			}
			denier.Deny(w, r, http.StatusUnauthorized)
		})
	}
}

// RequireAdmin enforces that the request's Principal (injected by Resolve)
// carries admin privileges: 401 when the request resolved anonymous, 403
// when it resolved to a non-admin principal — a member, or NSTR-23's account
// api key, which is never admin-equivalent (see
// domain.NewIntegrationPrincipal's own doc). This is the middleware NSTR-21's
// admin routes and NSTR-23's /settings/api-key gate mount behind; neither
// writes its own admin check.
func RequireAdmin(denier *Denier) middleware.Middleware {
	if denier == nil {
		panic("identity/adapter: RequireAdmin requires a non-nil Denier")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := CurrentPrincipal(r.Context())
			if !ok {
				denier.Deny(w, r, http.StatusUnauthorized)
				return
			}
			if !p.IsAdmin() {
				denier.Deny(w, r, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
