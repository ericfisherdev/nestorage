package app

import (
	"context"
	"errors"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// CredentialRevoker is the one-method outbound port AdminService depends on
// to invalidate a deactivated (or password-reset) user's outstanding
// credentials. It is deliberately narrow (ISP) and open for extension (OCP):
// NSTR-22 registers a device-token revoker against this exact port, in its
// own package, without editing this one.
type CredentialRevoker interface {
	// RevokeAll invalidates every outstanding credential belonging to id —
	// e.g. server-side sessions (adapter.SessionRevoker) or, from NSTR-22,
	// device tokens. Implementations must not treat "nothing to revoke" as
	// an error.
	RevokeAll(ctx context.Context, id domain.UserID) error
}

// Revokers fans a single RevokeAll call out to every registered
// CredentialRevoker and joins their errors: a failure in one revoker must
// not prevent the others from running, and none of them may be silently
// swallowed — AdminService surfaces the joined error to its caller rather
// than logging and discarding it.
type Revokers []CredentialRevoker

// Compile-time assurance Revokers itself satisfies the port it fans out to.
var _ CredentialRevoker = Revokers(nil)

// RevokeAll calls RevokeAll on every registered revoker and returns the
// joined result of any failures, or nil if all of them succeeded.
func (rs Revokers) RevokeAll(ctx context.Context, id domain.UserID) error {
	var errs []error
	for _, r := range rs {
		if err := r.RevokeAll(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
