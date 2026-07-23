package domain

import "fmt"

// Kind distinguishes a Principal resolved from a real person's credential
// (a session or device token) from one resolved from the account's
// integration credential (NSTR-23's api key), which has no user behind it.
// Stored only in memory — never persisted — but modeled as a typed enum
// anyway, mirroring how Role validates itself.
type Kind string

// The two kinds of principal NSTR-24's resolver can produce.
const (
	KindUser        Kind = "user"
	KindIntegration Kind = "integration"
)

// Valid reports whether k is a known Kind.
func (k Kind) Valid() bool {
	switch k {
	case KindUser, KindIntegration:
		return true
	default:
		return false
	}
}

// String returns the kind's stored value.
func (k Kind) String() string { return string(k) }

// ParseKind validates and returns a Kind, or a wrapped ErrInvalidKind naming
// the offending value.
func ParseKind(s string) (Kind, error) {
	k := Kind(s)
	if !k.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidKind, s)
	}
	return k, nil
}

// Principal is the one identity abstraction every authorization decision
// (CanSeeBin, CanMutateBin, IsAdmin) is made against, regardless of which of
// the three credentials — session (NSTR-20), device token (NSTR-22), or
// account api key (NSTR-23) — resolved it. UserID is the zero UserID for
// KindIntegration: there is no user behind the account's api key.
type Principal struct {
	Kind   Kind
	UserID UserID
	Role   Role
	Label  string
}

// NewUserPrincipal returns the Principal for a real person authenticated via
// session or device token: id and role come from the domain.User the
// credential resolved to, and label is that user's display name — the
// string item history attributes an action to.
func NewUserPrincipal(id UserID, role Role, label string) Principal {
	return Principal{Kind: KindUser, UserID: id, Role: role, Label: label}
}

// NewIntegrationPrincipal returns the Principal for NSTR-23's account api
// key: the Nestova integration authenticates as this, not as any household
// member. It hard-codes RoleMember — there is deliberately no way to build
// an admin integration through this constructor, which is what makes
// IsAdmin's two-part check correct even if a future bug ever set Role to
// RoleAdmin on one of these by some other path.
func NewIntegrationPrincipal(label string) Principal {
	return Principal{Kind: KindIntegration, Role: RoleMember, Label: label}
}

// IsAdmin reports whether p carries administrative privileges. Both parts of
// the check matter: Kind == KindUser is what stops a mis-constructed
// integration principal from ever reaching an admin route, even one whose
// Role somehow reads RoleAdmin — Role alone is not a sufficient check.
func (p Principal) IsAdmin() bool { return p.Kind == KindUser && p.Role == RoleAdmin }

// IsAnonymous reports whether p is the zero Principal — no credential
// resolved for this request.
func (p Principal) IsAnonymous() bool { return p == (Principal{}) }

// Actor returns the display label item history attributes an action to: a
// user's display name, or the account api key's label.
func (p Principal) Actor() string { return p.Label }
