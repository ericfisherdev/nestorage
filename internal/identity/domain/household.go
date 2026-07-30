package domain

import "time"

// Household is the tenant every member belongs to. Nestorage does not own
// household details beyond attachment (NSTR-116 covers who users are, not
// household-scoping of bins/items — a separate ticket in epic NSTR-112).
type Household struct {
	ID        HouseholdID
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ResolveExistingHousehold decides the household id to attach a new member
// to from an already-loaded list, under the adopt-only half of NSTR-116's
// household attachment rule (used once first-run setup has already run):
// exactly one household must already exist. Returns ErrNoHousehold when none
// exists — a genuine invariant violation, not a case to silently create one
// for — or ErrAmbiguousHousehold when several exist.
func ResolveExistingHousehold(households []Household) (HouseholdID, error) {
	switch len(households) {
	case 0:
		return HouseholdID{}, ErrNoHousehold
	case 1:
		return households[0].ID, nil
	default:
		return HouseholdID{}, ErrAmbiguousHousehold
	}
}
