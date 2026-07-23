package adapter

import (
	"log/slog"
	"net/http"

	"github.com/ericfisherdev/nestcore/httpserver/middleware"
	"github.com/ericfisherdev/nestcore/render"

	"github.com/ericfisherdev/nestorage/web/components"
)

// RequireAdmin enforces that the request's authenticated user (injected by
// Authenticate) carries the admin role. An HTMX request gets a bare 403 —
// mirroring RequireUser's own HX-Request branching; a full navigation gets a
// rendered forbidden page. A request with no authenticated user at all (an
// anonymous visitor) is rejected the same way as a non-admin one: this
// middleware is meant to run AFTER RequireUser in the admin route group's
// own chain (see cmd/server/main.go, where an anonymous request has already
// been redirected to /login by RequireUser), but it does not depend on that
// ordering for its own correctness.
//
// NSTR-24 re-homes this check onto the shared Principal model once it lands;
// the mount point (cmd/server/main.go's adminMux wiring) is the one place
// that changes — every route registered behind it stays untouched.
func RequireAdmin(logger *slog.Logger) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u, ok := CurrentUser(r.Context()); ok && u.IsAdmin() {
				next.ServeHTTP(w, r)
				return
			}
			if r.Header.Get("HX-Request") == "true" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if err := render.Render(r.Context(), w, http.StatusForbidden, components.ForbiddenPage()); err != nil {
				logger.ErrorContext(r.Context(), "require admin: render forbidden page", "error", err)
			}
		})
	}
}
