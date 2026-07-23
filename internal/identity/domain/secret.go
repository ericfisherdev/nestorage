package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
)

// secretBytes is the raw entropy of a generated bearer secret: 32 bytes (256
// bits), encoded as a 64-character hex string. This matches the project's
// other high-entropy-token sizing (see platform/session.csrfTokenLen).
const secretBytes = 32

// Generate returns a new random, hex-encoded bearer secret prefixed with
// prefix — e.g. "nsd_" for NSTR-22's device tokens, "ns_" for NSTR-23's
// account API key. The prefix makes the credential kind self-identifying, so
// NSTR-24's router can dispatch on it instead of probing every credential
// store in turn. This is the one place either ticket generates a secret, so
// the entropy and encoding stay defined in exactly one place.
//
// It errors only when the system's random source is unavailable, which is a
// fatal condition (mirrors crypto.Hash's own contract for the same failure).
func Generate(prefix string) (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("identity: generate secret: %w", err)
	}
	return prefix + hex.EncodeToString(b), nil
}

// Hash returns the SHA-256 hex digest of a raw bearer secret — the form
// persisted as *_token.token_hash (or equivalent) columns, never the secret
// itself.
//
// This deliberately does not use the argon2id KDF crypto.Hash applies to
// member passwords: a generated secret is 256 bits of crypto/rand output,
// not a human-chosen password, so it already carries full entropy and there
// is no dictionary or rainbow-table attack to defend against by stretching
// it. SHA-256 is the right tool for comparing a high-entropy random bearer
// secret, and avoids paying argon2's deliberate CPU/memory cost on every
// authenticated request.
func Hash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// SecretsMatch reports whether raw hashes to hash, using a constant-time
// comparison of the computed digest so secret verification does not leak
// timing information. The comparison stays constant-time even though the
// caller typically looks the row up by hash first — it costs nothing here,
// and keeps this function correct on its own if a future caller ever
// compares without a hash-keyed lookup.
func SecretsMatch(raw, hash string) bool {
	candidate := Hash(raw)
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(hash)) == 1
}
