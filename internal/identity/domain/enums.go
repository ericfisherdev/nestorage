package domain

import "fmt"

// Role is a user's role within the identity context. Stored as text,
// validated here.
type Role string

// User roles.
const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	switch r {
	case RoleAdmin, RoleMember:
		return true
	default:
		return false
	}
}

// IsAdmin reports whether r carries administrative privileges.
func (r Role) IsAdmin() bool { return r == RoleAdmin }

// String returns the role's stored value.
func (r Role) String() string { return string(r) }

// ParseRole validates and returns a Role, or a wrapped ErrInvalidRole naming
// the offending value.
func ParseRole(s string) (Role, error) {
	r := Role(s)
	if !r.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidRole, s)
	}
	return r, nil
}

// UserColor is one of the four owner palette keys a user can be assigned.
// The value matches the Tailwind owner-color token infix (see
// web/components/owner.templ), so a user renders as bg-owner-<color>-tint
// etc.
type UserColor string

// The four owner palette colors assignable to a user. "shared" is
// deliberately not here: it is the Family/unowned sentinel used by bin
// ownership (Sprint 4), where an unowned bin is a null owner rather than a
// user wearing a special color.
const (
	ColorIndigo UserColor = "indigo"
	ColorSteel  UserColor = "steel"
	ColorTeal   UserColor = "teal"
	ColorPeri   UserColor = "peri"
)

// Valid reports whether c is a known, user-assignable palette color.
func (c UserColor) Valid() bool {
	switch c {
	case ColorIndigo, ColorSteel, ColorTeal, ColorPeri:
		return true
	default:
		return false
	}
}

// String returns the color's stored value.
func (c UserColor) String() string { return string(c) }

// ParseUserColor validates and returns a UserColor, or a wrapped
// ErrInvalidColor naming the offending value. A value of "shared" is rejected
// here even though it is a valid owner palette key elsewhere — see UserColor's
// own doc.
func ParseUserColor(s string) (UserColor, error) {
	c := UserColor(s)
	if !c.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidColor, s)
	}
	return c, nil
}
