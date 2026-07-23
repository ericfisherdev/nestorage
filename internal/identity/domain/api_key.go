package domain

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// APIKeyPrefix marks a bearer credential as the account's single api key
// rather than one of NSTR-22's per-device tokens (DeviceTokenPrefix,
// "nsd_"). The two are safely distinguishable by strings.HasPrefix despite
// sharing a leading "ns": "ns_" requires an underscore at index 2, where
// "nsd_" has 'd' — so a device token never matches this prefix and vice
// versa. NSTR-24's router relies on that distinction to dispatch a
// presented bearer credential to the right resolver without probing every
// credential store in turn.
const APIKeyPrefix = "ns_"

// GenerateAPIKeySecret returns a new random account api key secret, "ns_"
// followed by 64 lowercase hex characters. It delegates to the shared
// bearer-secret primitive (Generate) NSTR-22's device tokens also use, so
// the entropy and encoding are defined once for every credential kind in
// this context.
func GenerateAPIKeySecret() (string, error) { return Generate(APIKeyPrefix) }

// HashAPIKeySecret returns the SHA-256 hex digest of a raw api key secret,
// the form persisted in api_key.secret_hash. See Hash's doc for why
// SHA-256 rather than argon2id is the right tool here.
func HashAPIKeySecret(raw string) string { return Hash(raw) }

// APIKeySecretsMatch reports whether raw hashes to hash, using a
// constant-time comparison. See SecretsMatch's doc.
func APIKeySecretsMatch(raw, hash string) bool { return SecretsMatch(raw, hash) }

// keyPrefixDisplayHexLen is how many hex characters of a generated secret
// are kept in KeyPrefixOf's result — enough for the admin screen to
// recognize which key a row is, without exposing enough of the secret to
// narrow a brute-force search.
const keyPrefixDisplayHexLen = 8

// KeyPrefixOf returns raw's non-secret display fragment: the "ns_" prefix
// plus its first 8 hex characters, e.g. "ns_a1b2c3d4". This is what
// api_key.key_prefix stores and the admin screen renders — the secret
// itself is never stored or shown again after the response that created or
// rotated it.
func KeyPrefixOf(raw string) string {
	n := len(APIKeyPrefix) + keyPrefixDisplayHexLen
	if len(raw) < n {
		return raw
	}
	return raw[:n]
}

// APIKeyStatus is the point-in-time state APIKey.Status derives from
// RevokedAt/ExpiresAt — not stored, always computed against a caller-
// supplied now so it stays correct without a background job flipping rows.
type APIKeyStatus string

// The api key lifecycle states. Exactly one applies at any given now.
const (
	// APIKeyStatusCurrent is the one key api_key_current_uniq allows to
	// exist unsuperseded: RevokedAt and ExpiresAt are both nil.
	APIKeyStatusCurrent APIKeyStatus = "current"
	// APIKeyStatusRetiring is a key superseded by a rotation but still
	// inside its chosen overlap window: ExpiresAt is set and in the future.
	APIKeyStatusRetiring APIKeyStatus = "retiring"
	// APIKeyStatusExpired is a key whose overlap window has passed:
	// ExpiresAt is set and no longer in the future. Not revoked.
	APIKeyStatusExpired APIKeyStatus = "expired"
	// APIKeyStatusRevoked is a key an admin explicitly revoked: RevokedAt
	// is set. Takes precedence over an expired ExpiresAt.
	APIKeyStatusRevoked APIKeyStatus = "revoked"
)

// String returns the status's stored/rendered value.
func (s APIKeyStatus) String() string { return string(s) }

// OverlapWindow is the caller's choice, at rotation, for how long the
// superseded key stays usable before it stops authenticating. It is a
// bounded domain constant set rather than a free-form duration or an
// environment variable: nothing about the choice needs to differ per
// deployment, and a bounded set is what lets the rotate form render a
// closed <select> instead of a free-text duration field.
type OverlapWindow string

// The overlap windows a rotation may choose. MaxOverlapWindow is the
// longest of them (Overlap7d's duration), exported for a caller that needs
// to reason about the widest possible retiring window without hard-coding
// which named choice currently maps to it.
const (
	OverlapNone OverlapWindow = "none"
	Overlap24h  OverlapWindow = "24h"
	Overlap7d   OverlapWindow = "7d"

	MaxOverlapWindow = 7 * 24 * time.Hour
)

// String returns the window's stored value.
func (w OverlapWindow) String() string { return string(w) }

// Valid reports whether w is a known overlap window.
func (w OverlapWindow) Valid() bool {
	switch w {
	case OverlapNone, Overlap24h, Overlap7d:
		return true
	default:
		return false
	}
}

// Duration returns the time.Duration w represents, zero for OverlapNone.
// Only meaningful for a Valid w — ParseOverlapWindow is the one place an
// untrusted string becomes an OverlapWindow, so every other caller already
// holds a validated value.
func (w OverlapWindow) Duration() time.Duration {
	switch w {
	case Overlap24h:
		return 24 * time.Hour
	case Overlap7d:
		return 7 * 24 * time.Hour
	default:
		// OverlapNone, and any invalid value ParseOverlapWindow would have
		// already rejected, both mean "no overlap".
		return 0
	}
}

// ParseOverlapWindow validates and returns an OverlapWindow, or a wrapped
// ErrInvalidOverlapWindow naming the offending value.
func ParseOverlapWindow(s string) (OverlapWindow, error) {
	w := OverlapWindow(s)
	if !w.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidOverlapWindow, s)
	}
	return w, nil
}

// APIKey is the account's single bearer credential for machine-to-machine
// calls (NSTR-23): the Nestova integration authenticates as this key,
// resolving to a system principal rather than to any person. SecretHash is
// the SHA-256 hex digest of the presented secret (see HashAPIKeySecret); the
// raw secret is never persisted and is returned to the caller exactly once,
// at creation or rotation. ExpiresAt is nil until a rotation supersedes this
// row, then holds the end of the chosen overlap window. RevokedAt is nil
// unless an admin explicitly revoked the key.
type APIKey struct {
	ID         APIKeyID
	KeyPrefix  string
	SecretHash string
	Label      string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
}

// Validate reports whether the key is well-formed, wrapping
// ErrInvalidAPIKey.
func (k *APIKey) Validate() error {
	if k.ID == (APIKeyID{}) {
		return fmt.Errorf("%w: id is required", ErrInvalidAPIKey)
	}
	if strings.TrimSpace(k.KeyPrefix) == "" {
		return fmt.Errorf("%w: key prefix must not be blank", ErrInvalidAPIKey)
	}
	if strings.TrimSpace(k.SecretHash) == "" {
		return fmt.Errorf("%w: secret hash must not be blank", ErrInvalidAPIKey)
	}
	if strings.TrimSpace(k.Label) == "" {
		return fmt.Errorf("%w: label must not be blank", ErrInvalidAPIKey)
	}
	return nil
}

// Usable reports whether the key still authenticates at now: not revoked,
// and either never superseded (ExpiresAt nil) or still inside its overlap
// window (ExpiresAt after now).
func (k *APIKey) Usable(now time.Time) bool {
	if k.RevokedAt != nil {
		return false
	}
	return k.ExpiresAt == nil || k.ExpiresAt.After(now)
}

// Status reports the key's lifecycle state at now. See APIKeyStatus's own
// doc for what each value means.
func (k *APIKey) Status(now time.Time) APIKeyStatus {
	switch {
	case k.RevokedAt != nil:
		return APIKeyStatusRevoked
	case k.ExpiresAt == nil:
		return APIKeyStatusCurrent
	case k.ExpiresAt.After(now):
		return APIKeyStatusRetiring
	default:
		return APIKeyStatusExpired
	}
}

// APIKeyRepository is the outbound port for persisting and retrieving the
// account's api key. Implementations live in the adapter package.
//
// Error contracts:
//   - Create returns ErrAPIKeyExists when a current key already exists —
//     enforced by the api_key_current_uniq partial unique index, not only
//     by application code.
//   - GetBySecretHash returns the key regardless of revocation/expiry
//     state, so the caller can distinguish "unknown secret"
//     (ErrAPIKeyNotFound) from "known but revoked/expired" (checked via
//     Usable/Status) and log/react to each differently.
//   - ListAll returns every key (current, retiring, expired, and revoked)
//     newest first, or an empty slice when none exist — the admin screen's
//     history view.
//   - Rotate atomically supersedes incumbentID (setting its ExpiresAt, if
//     incumbentID is non-zero) and inserts next, in one transaction: if the
//     insert fails, the incumbent is left un-superseded rather than the
//     account being briefly left with no usable key. Returns
//     ErrAPIKeyNotFound when incumbentID is non-zero but does not name an
//     unrevoked row.
//   - Revoke sets RevokedAt to now for id. Returns ErrAPIKeyNotFound when
//     id is unknown or already revoked — a second Revoke on the same key is
//     not silently a no-op.
//   - TouchLastUsed updates LastUsedAt for id to now, but only when the
//     existing value is NULL or older than staleBefore — the write is
//     throttled by the caller passing staleBefore = now minus an interval,
//     so a key authenticating repeatedly does not write every request. A
//     missing id is not an error: an authentication path must never fail
//     because this best-effort bookkeeping found nothing to touch.
type APIKeyRepository interface {
	Create(ctx context.Context, k *APIKey) error
	GetBySecretHash(ctx context.Context, secretHash string) (*APIKey, error)
	ListAll(ctx context.Context) ([]*APIKey, error)
	Rotate(ctx context.Context, incumbentID APIKeyID, expiresAt time.Time, next *APIKey) error
	Revoke(ctx context.Context, id APIKeyID, revokedAt time.Time) error
	TouchLastUsed(ctx context.Context, id APIKeyID, now, staleBefore time.Time) error
}
