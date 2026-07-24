package domain

import (
	"fmt"
	"strings"
)

// StorageBackend records which domain.PhotoStore implementation persisted a
// photo's bytes. This is a repository-level fact stamped onto every row at
// Create, not something a caller chooses per photo — mirrors Nestova's
// identical NES-132 column/rationale. NSTR-35 adds StorageBackendS3 here —
// the enum exists now, with its own ParseStorageBackend default case,
// precisely so that ticket is additive and never has to touch this file's
// existing values.
type StorageBackend string

// StorageBackendLocal is the only StorageBackend this ticket supports — the
// zero-config default every fresh install gets. NSTR-35 adds
// StorageBackendS3.
const (
	StorageBackendLocal StorageBackend = "local"
)

// Valid reports whether b is a known StorageBackend.
func (b StorageBackend) Valid() bool {
	switch b {
	case StorageBackendLocal:
		return true
	default:
		return false
	}
}

// String returns b's storage/column spelling.
func (b StorageBackend) String() string { return string(b) }

// ParseStorageBackend parses a persisted storage_backend column value,
// rejecting anything unknown rather than trusting the database blindly —
// the default switch case is deliberate: it is what lets NSTR-35 add
// StorageBackendS3 here without this function's shape changing.
func ParseStorageBackend(s string) (StorageBackend, error) {
	b := StorageBackend(strings.ToLower(strings.TrimSpace(s)))
	switch b {
	case StorageBackendLocal:
		return b, nil
	default:
		return "", fmt.Errorf("media: unknown storage backend %q", s)
	}
}
