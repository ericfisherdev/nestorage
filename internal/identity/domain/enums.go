package domain

import "fmt"

// Role is a member's role within the identity context: the unified
// owner/adult/child vocabulary shared with Nestova (epic NSTR-112) rather
// than an app-owned admin/member vocabulary. Stored as text, validated here.
type Role string

// The unified household roles. Apps own no roles of their own: Nestorage
// derives admin-versus-member from these, never the reverse — owner is
// admin, adult and child are member-level (see IsAdmin). child is treated
// exactly as member-level for now; any tighter restriction is future work,
// not designed here.
const (
	RoleOwner Role = "owner"
	RoleAdult Role = "adult"
	RoleChild Role = "child"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	switch r {
	case RoleOwner, RoleAdult, RoleChild:
		return true
	default:
		return false
	}
}

// IsAdmin reports whether r carries administrative privileges: only
// RoleOwner does. adult and child are both member-level.
func (r Role) IsAdmin() bool { return r == RoleOwner }

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
