package domain

import "strings"

// maxLocationNameRunes is the longest location name ValidateLocationName
// accepts, counted by rune (not byte) so a multi-byte character counts once,
// not per byte — the same reasoning as identity's password rune bounds.
const maxLocationNameRunes = 100

// ValidateLocationName trims surrounding whitespace and returns the trimmed
// name, or a wrapped ErrInvalidLocationName when the trimmed result is empty
// (including whitespace-only input) or exceeds maxLocationNameRunes. This is
// the storage context's identity.ValidatePassword/NormalizeEmail analog: the
// one place the name rule is defined, called by every write path. The
// LocationRepository does not re-validate on write — the caller is
// responsible for calling this first.
func ValidateLocationName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", ErrInvalidLocationName
	}
	if len([]rune(trimmed)) > maxLocationNameRunes {
		return "", ErrInvalidLocationName
	}
	return trimmed, nil
}
