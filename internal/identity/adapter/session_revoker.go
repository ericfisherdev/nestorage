package adapter

import (
	"context"
	"fmt"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestorage/internal/identity/app"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
)

// SessionRevoker implements app.CredentialRevoker over an *scs.SessionManager:
// RevokeAll destroys every active session belonging to the target user, so a
// request racing a deactivation or password reset cannot keep using a
// session the admin action was supposed to end.
type SessionRevoker struct {
	sm *scs.SessionManager
}

// Compile-time assurance the adapter satisfies the port.
var _ app.CredentialRevoker = (*SessionRevoker)(nil)

// NewSessionRevoker constructs a SessionRevoker over sm. Panics on a nil sm,
// matching every other constructor in this codebase.
func NewSessionRevoker(sm *scs.SessionManager) *SessionRevoker {
	if sm == nil {
		panic("identity/adapter: NewSessionRevoker requires a non-nil session manager")
	}
	return &SessionRevoker{sm: sm}
}

// RevokeAll iterates every active session in the store and destroys the ones
// belonging to id. sm.Iterate requires the store to implement
// scs.IterableStore — verified against the vendored pgxstore.PostgresStore
// (it defines both All and AllCtx) — and panics if it does not, which is a
// wiring bug the composition root should catch immediately, not a runtime
// condition this method needs to guard against.
func (r *SessionRevoker) RevokeAll(ctx context.Context, id domain.UserID) error {
	target := id.String()
	err := r.sm.Iterate(ctx, func(ctx context.Context) error {
		if r.sm.GetString(ctx, session.KeyUserID) != target {
			return nil
		}
		return r.sm.Destroy(ctx)
	})
	if err != nil {
		return fmt.Errorf("session revoker: revoke all: %w", err)
	}
	return nil
}
