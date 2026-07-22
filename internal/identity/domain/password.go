package domain

// Password length bounds, in runes (not bytes — a multi-byte character must
// count once, not per byte). NIST SP 800-63B favors length over composition
// rules, so ValidatePassword enforces only these bounds; no required
// character classes.
const (
	// minPasswordRunes is the shortest password ValidatePassword accepts.
	minPasswordRunes = 12
	// maxPasswordRunes is the longest password ValidatePassword accepts. It
	// bounds the cost of the argon2id derivation, which is otherwise
	// unbounded for arbitrarily long input.
	maxPasswordRunes = 128
)

// ValidatePassword enforces the identity context's one password policy: 12
// to 128 runes, no composition rules. NSTR-19's first-run wizard and
// NSTR-20/21's password-change flows all call this same function, so the
// rule is defined and changed in exactly one place.
func ValidatePassword(password string) error {
	n := len([]rune(password))
	switch {
	case n < minPasswordRunes:
		return ErrPasswordTooShort
	case n > maxPasswordRunes:
		return ErrPasswordTooLong
	default:
		return nil
	}
}
