package adapter

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/ericfisherdev/nestcore/httpserver/middleware"
)

// setupPath is the first-run onboarding wizard's route.
const setupPath = "/setup"

// setupExemptPrefixes are the path prefixes SetupGuard never blocks, even
// before setup is complete: /setup itself (obviously — the wizard has to be
// reachable), /static/ (else the wizard renders unstyled), and the platform
// probes (else the appliance looks unhealthy while unconfigured).
var setupExemptPrefixes = []string{setupPath, "/static/", "/healthz", "/readyz", "/metrics"}

// firstUserChecker is the narrow read port (ISP) SetupGuard depends on:
// only the first-run existence check, satisfied by domain.UserRepository (a
// superset) and by test fakes.
type firstUserChecker interface {
	HasAnyUser(ctx context.Context) (bool, error)
}

// SetupGuard redirects every non-exempt request to /setup until the first
// admin exists, then latches permanently open. It must be the outermost
// feature middleware (installed before NSTR-24's principal resolution) so
// an unauthenticated visitor is sent to the wizard before anything else
// runs.
func SetupGuard(repo firstUserChecker, logger *slog.Logger) middleware.Middleware {
	// complete is shared across every request through this one closure —
	// SetupGuard is called once at wiring time, not per request. Once set,
	// it short-circuits every later request with no database query at all:
	// setup completion is irreversible by design (NSTR-21 deactivates users
	// rather than deleting them), so the latch can never go stale.
	var complete atomic.Bool

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isSetupExempt(r.URL.Path) || complete.Load() {
				next.ServeHTTP(w, r)
				return
			}

			has, err := repo.HasAnyUser(r.Context())
			if err != nil {
				logger.ErrorContext(r.Context(), "setup guard: check existing users", "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			if has {
				complete.Store(true)
				next.ServeHTTP(w, r)
				return
			}
			redirectToSetup(w, r)
		})
	}
}

// isSetupExempt reports whether path is exempt from the setup guard.
func isSetupExempt(path string) bool {
	for _, prefix := range setupExemptPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// redirectToSetup sends the client to /setup. An HTMX partial request gets
// HX-Redirect (204) so htmx performs a full navigation instead of trying to
// swap the wizard into whatever fragment target it was aiming at; a full
// navigation gets a plain 303.
func redirectToSetup(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", setupPath)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, setupPath, http.StatusSeeOther)
}
