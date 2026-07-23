package domain

// BinSubject is the narrow view of a bin CanSeeBin and CanMutateBin need to
// decide visibility. It is declared here, in identity, rather than imported
// from a bins package, because bins/domain.Bin (NSTR-27) does not exist yet
// and this package must never depend on it in either direction: the
// dependency points from bins to identity, never back. NSTR-27's Bin
// satisfies this interface by adding the two accessors below.
type BinSubject interface {
	// BinCreator returns the id of the user who created the bin.
	BinCreator() UserID
	// BinPrivate reports whether the bin is visible only to its creator and
	// admins, rather than to every principal.
	BinPrivate() bool
}

// CanSeeBin reports whether p may see b: true when b is public, when p is an
// admin, or when p is the user who created it. An integration principal
// (NSTR-23's account api key) satisfies none of the user-scoped clauses —
// see NewIntegrationPrincipal's own doc for why that is deliberate: Nestova
// cannot read a household member's private bin through the account api key.
func CanSeeBin(p Principal, b BinSubject) bool {
	if !b.BinPrivate() {
		return true
	}
	if p.IsAdmin() {
		return true
	}
	return p.Kind == KindUser && p.UserID == b.BinCreator()
}

// CanMutateBin reports whether p may change b. Today this is the exact same
// rule as CanSeeBin, but kept as its own function — not an alias — so a
// later ticket can tighten mutation without touching read visibility.
func CanMutateBin(p Principal, b BinSubject) bool {
	return CanSeeBin(p, b)
}
