package adapter

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
)

// sessionResolver wraps NSTR-20's session-cookie login into a Resolver:
// Chain only ever calls this when the request carries no Authorization
// header, so it never needs to check for one itself.
type sessionResolver struct {
	sm     *scs.SessionManager
	users  currentUserFinder
	logger *slog.Logger
}

// NewSessionResolver constructs the Resolver Chain falls back to when no
// bearer credential is presented. All dependencies are required; a missing
// one panics at construction time, matching every other constructor in this
// package.
func NewSessionResolver(sm *scs.SessionManager, users currentUserFinder, logger *slog.Logger) Resolver {
	if sm == nil {
		panic("identity/adapter: NewSessionResolver requires a non-nil session manager")
	}
	if users == nil {
		panic("identity/adapter: NewSessionResolver requires a non-nil currentUserFinder")
	}
	if logger == nil {
		panic("identity/adapter: NewSessionResolver requires a non-nil logger")
	}
	return &sessionResolver{sm: sm, users: users, logger: logger}
}

// Resolve reads the session's stored user id and wraps it into a user
// Principal, reusing resolveSessionUser — the same lookup and stale-key
// cleanup Authenticate applies for the session-based *domain.User path. An
// absent session, or one naming an unknown or deactivated user, reports
// not-found rather than an error: a missing or stale session cookie is not
// an invalid credential the way a malformed bearer secret is.
func (sr *sessionResolver) Resolve(ctx context.Context, _ *http.Request) (domain.Principal, bool, error) {
	idStr := sr.sm.GetString(ctx, session.KeyUserID)
	if idStr == "" {
		return domain.Principal{}, false, nil
	}
	u, ok := resolveSessionUser(ctx, sr.sm, sr.users, sr.logger, idStr)
	if !ok {
		return domain.Principal{}, false, nil
	}
	return domain.NewUserPrincipal(u.ID, u.Role, u.DisplayName), true, nil
}
