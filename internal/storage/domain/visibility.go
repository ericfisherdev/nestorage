package domain

import "fmt"

// Visibility controls who may see and mutate a bin: public (default, every
// principal) or private (only its creator and admins) — the rule
// identity.CanSeeBin/CanMutateBin define and BinRepository's SQL predicate
// mirrors exactly.
type Visibility string

// The two bin visibility states.
const (
	VisibilityPublic  Visibility = "public"
	VisibilityPrivate Visibility = "private"
)

// Valid reports whether v is a known Visibility.
func (v Visibility) Valid() bool {
	switch v {
	case VisibilityPublic, VisibilityPrivate:
		return true
	default:
		return false
	}
}

// IsPrivate reports whether v restricts visibility to the bin's creator and
// admins — the value Bin.BinPrivate (identity.BinSubject) returns.
func (v Visibility) IsPrivate() bool { return v == VisibilityPrivate }

// String returns the visibility's stored value.
func (v Visibility) String() string { return string(v) }

// ParseVisibility validates and returns a Visibility, or a wrapped
// ErrInvalidVisibility naming the offending value.
func ParseVisibility(s string) (Visibility, error) {
	v := Visibility(s)
	if !v.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidVisibility, s)
	}
	return v, nil
}
