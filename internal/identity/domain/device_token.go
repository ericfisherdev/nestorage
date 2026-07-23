package domain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// DeviceTokenID uniquely identifies a device token.
type DeviceTokenID uuid.UUID

// NewDeviceTokenID returns a new time-ordered (UUIDv7) device token id,
// mirroring NewUserID's rationale: better B-tree index locality than a
// random v4 id.
func NewDeviceTokenID() DeviceTokenID { return DeviceTokenID(uuid.Must(uuid.NewV7())) }

// String returns the canonical UUID string.
func (id DeviceTokenID) String() string { return uuid.UUID(id).String() }

// ParseDeviceTokenID parses a canonical UUID string into a DeviceTokenID.
func ParseDeviceTokenID(s string) (DeviceTokenID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return DeviceTokenID{}, fmt.Errorf("parse device token id: %w", err)
	}
	return DeviceTokenID(u), nil
}

// DeviceTokenPrefix marks a bearer credential as a device token rather than
// NSTR-23's account API key ("ns_"), so NSTR-24's resolver can dispatch on
// the prefix instead of probing both credential stores.
const DeviceTokenPrefix = "nsd_"

// GenerateDeviceToken returns a new random device token, "nsd_" followed by
// 64 lowercase hex characters. It delegates to the shared bearer-secret
// primitive (Generate) NSTR-23's account API key also uses, so the entropy
// and encoding are defined once for every credential kind in this context.
func GenerateDeviceToken() (string, error) { return Generate(DeviceTokenPrefix) }

// HashDeviceToken returns the SHA-256 hex digest of a raw device token, the
// form persisted in device_token.token_hash. See Hash's doc for why SHA-256
// rather than argon2id is the right tool here.
func HashDeviceToken(raw string) string { return Hash(raw) }

// DeviceTokensMatch reports whether raw hashes to hash, using a
// constant-time comparison. See SecretsMatch's doc.
func DeviceTokensMatch(raw, hash string) bool { return SecretsMatch(raw, hash) }

// DeviceToken is a bearer credential issued to one of a user's devices (the
// Android client). TokenHash is the SHA-256 hex digest of the presented
// token (see HashDeviceToken); the raw token is never persisted and is
// returned to the caller exactly once, at creation. RevokedAt is nil while
// the token is active.
type DeviceToken struct {
	ID         DeviceTokenID
	UserID     UserID
	TokenHash  string
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// Validate reports whether the token is well-formed, wrapping
// ErrInvalidDeviceToken.
func (t *DeviceToken) Validate() error {
	if t.ID == (DeviceTokenID{}) {
		return fmt.Errorf("%w: id is required", ErrInvalidDeviceToken)
	}
	if t.UserID == (UserID{}) {
		return fmt.Errorf("%w: user id is required", ErrInvalidDeviceToken)
	}
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("%w: name must not be blank", ErrInvalidDeviceToken)
	}
	if strings.TrimSpace(t.TokenHash) == "" {
		return fmt.Errorf("%w: token hash must not be blank", ErrInvalidDeviceToken)
	}
	return nil
}

// Active reports whether the token is still usable.
func (t *DeviceToken) Active() bool { return t.RevokedAt == nil }

// DeviceTokenRepository is the outbound port for persisting and retrieving
// device tokens. Implementations live in the adapter package.
//
// Error contracts:
//   - Create returns a wrapped error if the user id is unknown (an
//     app_user foreign-key violation); TokenHash collisions are
//     astronomically unlikely (256 bits of crypto/rand) but are still
//     surfaced rather than silently overwritten.
//   - GetByTokenHash returns the token regardless of revocation state, so
//     the caller can distinguish "unknown token" (ErrDeviceTokenNotFound)
//     from "known but revoked" (checked via Active()) and log/react to each
//     differently.
//   - ListByUser returns the user's tokens newest first, or an empty slice
//     when none exist.
//   - Revoke is scoped by userID in addition to id, so a token cannot be
//     revoked by anyone but its owner — an IDOR is impossible in SQL, not
//     merely checked in a handler. Returns ErrDeviceTokenNotFound when id is
//     unknown for that user or already revoked.
//   - RevokeAllForUser revokes every active token for userID and reports how
//     many were revoked. "Nothing to revoke" is success (0, nil), not an
//     error — NSTR-21's Deactivate calls this unconditionally.
//   - TouchLastUsed updates last_used_at for id to now, but only when the
//     existing value is NULL or older than staleBefore — the write is
//     throttled by the caller passing staleBefore = now minus an interval,
//     so a device authenticating repeatedly does not write every request.
//     A missing id is not an error: an authentication path must never fail
//     because the best-effort last-used bookkeeping found nothing to touch.
type DeviceTokenRepository interface {
	Create(ctx context.Context, t *DeviceToken) error
	GetByTokenHash(ctx context.Context, tokenHash string) (*DeviceToken, error)
	ListByUser(ctx context.Context, userID UserID) ([]*DeviceToken, error)
	Revoke(ctx context.Context, userID UserID, id DeviceTokenID, revokedAt time.Time) error
	RevokeAllForUser(ctx context.Context, userID UserID, revokedAt time.Time) (int64, error)
	TouchLastUsed(ctx context.Context, id DeviceTokenID, now, staleBefore time.Time) error
}
