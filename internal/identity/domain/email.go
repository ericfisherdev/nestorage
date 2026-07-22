package domain

import "strings"

// NormalizeEmail trims surrounding whitespace and lowercases email, so the
// wizard and every other write path agree with the app_user.email column's
// own case-insensitive comparison (citext) on what "the same address"
// means, rather than relying on citext alone to catch a mismatch after the
// fact.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
