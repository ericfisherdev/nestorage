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
// scs.IterableStore — verified against nestcore's identity/session store
// (it defines AllCtx) — and panics if it does not, which is a wiring bug the
// composition root should catch immediately, not a runtime condition this
// method needs to guard against.
//
// This deletes via r.sm.Store directly (r.sm.Token(ctx), the key scs.Iterate
// already read out of the store) rather than calling r.sm.Destroy(ctx): with
// HashTokenInStore set (required for the shared store — see nestcore's
// identity/session package doc), scs's own Iterate populates the iterated
// context's token from the store's ALREADY-HASHED key, but Destroy hashes
// whatever token it is given AGAIN before deleting — verified against
// alexedwards/scs v2.9.0's doStoreDelete. Calling Destroy from inside
// Iterate's callback therefore looks up a hash-of-a-hash that matches no
// row, silently deleting nothing. Deleting through the store directly with
// the token Iterate already gave us — no second hash — is what actually
// works. Checks for DeleteCtx alone (mirroring scs's own doStoreDelete,
// which checks this same narrower interface) rather than the full
// scs.CtxStore — a store implementing DeleteCtx but not FindCtx/CommitCtx
// would otherwise wrongly fall through to the plain Store.Delete path
// below, which is only there for test doubles built on scs.New()'s default
// in-memory store, which implements just the non-context Store interface.
func (r *SessionRevoker) RevokeAll(ctx context.Context, id domain.UserID) error {
	target := id.String()
	err := r.sm.Iterate(ctx, func(ctx context.Context) error {
		if r.sm.GetString(ctx, session.KeyUserID) != target {
			return nil
		}
		token := r.sm.Token(ctx)
		if c, ok := r.sm.Store.(interface {
			DeleteCtx(context.Context, string) error
		}); ok {
			return c.DeleteCtx(ctx, token)
		}
		return r.sm.Store.Delete(token)
	})
	if err != nil {
		return fmt.Errorf("session revoker: revoke all: %w", err)
	}
	return nil
}
