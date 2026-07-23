package adapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestcore/httpserver/middleware"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
)

// currentUserContextKey is the unexported type for the context key
// Authenticate stores the resolved user under, so it cannot collide with a
// key from another package.
type currentUserContextKey struct{}

// currentUserFinder is the narrow read port (ISP) Authenticate depends on:
// only the by-id lookup it needs, satisfied by domain.UserRepository (a
// superset) and by test fakes.
type currentUserFinder interface {
	FindByID(ctx context.Context, id domain.UserID) (*domain.User, error)
}

// CurrentUser returns the User Authenticate placed in ctx, and false when no
// authenticated user is present — either an anonymous request, or one
// served before Authenticate runs.
func CurrentUser(ctx context.Context) (*domain.User, bool) {
	u, ok := ctx.Value(currentUserContextKey{}).(*domain.User)
	return u, ok
}

// resolveSessionUser resolves idStr (the session's stored user id) into a
// domain.User, clearing the stale session key when the id is malformed or
// names a user that no longer exists or has been deactivated. A transient
// repository error is logged and leaves the session intact — see
// Authenticate's own doc for why that case must not clear the key. Returns
// ok=false whenever no user should be attached to the request context.
func resolveSessionUser(ctx context.Context, sm *scs.SessionManager, repo currentUserFinder, logger *slog.Logger, idStr string) (u *domain.User, ok bool) {
	id, err := domain.ParseUserID(idStr)
	if err != nil {
		// A malformed id can never become valid.
		sm.Remove(ctx, session.KeyUserID)
		return nil, false
	}

	u, err = repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			sm.Remove(ctx, session.KeyUserID)
		} else {
			logger.ErrorContext(ctx, "authenticate: load current user", "error", err)
		}
		return nil, false
	}
	if !u.Active {
		sm.Remove(ctx, session.KeyUserID)
		return nil, false
	}
	return u, true
}

// Authenticate is a middleware that resolves the session's stored user id
// (session.KeyUserID) into a domain.User and places it in the request
// context for CurrentUser/RequireUser to read back. Requests without a
// session, or naming an unknown/inactive user, proceed unchanged so that
// public routes (the login page, static assets) keep working; RequireUser
// enforces authentication for protected routes.
//
// It never rejects a request outright: a transient repository error
// proceeds anonymously and leaves the session intact for a later retry;
// only a genuine "user no longer exists" or "user deactivated" outcome
// clears the stale key, so an anonymous request does not repeat the failed
// lookup on every subsequent request. See resolveSessionUser for that
// decision.
func Authenticate(sm *scs.SessionManager, repo currentUserFinder, logger *slog.Logger) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			idStr := sm.GetString(r.Context(), session.KeyUserID)
			if idStr == "" {
				next.ServeHTTP(w, r)
				return
			}
			if u, ok := resolveSessionUser(r.Context(), sm, repo, logger, idStr); ok {
				r = r.WithContext(context.WithValue(r.Context(), currentUserContextKey{}, u))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireUser enforces that a User is present in the request context
// (injected by Authenticate). An HTMX partial request gets a bare 401;
// a full navigation redirects to /login with the original path in the next
// query parameter for the post-login redirect.
func RequireUser() middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := CurrentUser(r.Context()); ok {
				next.ServeHTTP(w, r)
				return
			}
			if r.Header.Get("HX-Request") == "true" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			target := "/login?next=" + url.QueryEscape(r.URL.RequestURI())
			http.Redirect(w, r, target, http.StatusSeeOther)
		})
	}
}
