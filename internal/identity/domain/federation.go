package domain

import (
	"fmt"
	"strings"
	"time"
)

// maxFederationIDRunes bounds MemberLink's opaque provider identifiers
// (member id, household id): non-blank, at most this many characters.
// Nestorage never parses these strings — it only stores and compares them
// verbatim — so the only validation it can do is "non-blank, not
// pathologically long", mirroring app.validateAPIKeyLabel's identical
// "count runes, bound the length" shape.
const maxFederationIDRunes = 200

// MemberLink ties a Nestova member to a Nestorage user (NSTR-101). MemberID
// and HouseholdID are opaque provider identifiers — deliberately never
// UserID itself: conflating them would let a re-push renumber a user and
// orphan every created_by reference already pointing at it (see this type's
// own migration, 00017_provider_member_link.sql).
type MemberLink struct {
	ID          MemberLinkID
	UserID      UserID
	MemberID    string
	HouseholdID string
	LinkedAt    time.Time
}

// HouseholdBinding is the single Nestova household this Nestorage install is
// bound to, once an attach call has established it (NSTR-106). Recorded in
// the database, not configuration — see federation_repository.go's own doc.
type HouseholdBinding struct {
	HouseholdID string
	BoundAt     time.Time
}

// FederationProfile carries the profile fields FederationProvisioner.Upsert
// either creates a federated user from, or applies to an already-linked
// one. Role must already be a validated domain.Role (ParseRole is the
// caller's job, mirroring every other endpoint that accepts a role string).
type FederationProfile struct {
	DisplayName string
	Email       string
	Role        Role
	Active      bool
}

// ValidateMemberID validates a Nestova member identifier: non-blank, at
// most maxFederationIDRunes characters. Returns a wrapped
// ErrInvalidMemberLink naming the field.
func ValidateMemberID(memberID string) error {
	return validateFederationID(memberID, "member_id")
}

// ValidateHouseholdID validates a Nestova household identifier — the same
// non-blank/length rule ValidateMemberID applies, to a different field.
func ValidateHouseholdID(householdID string) error {
	return validateFederationID(householdID, "household_id")
}

// validateFederationID is ValidateMemberID/ValidateHouseholdID's shared
// rule: trim, then reject blank or over-long — field names the offending
// parameter in the wrapped error.
func validateFederationID(id, field string) error {
	n := len([]rune(strings.TrimSpace(id)))
	switch {
	case n == 0:
		return fmt.Errorf("%w: %s must not be blank", ErrInvalidMemberLink, field)
	case n > maxFederationIDRunes:
		return fmt.Errorf("%w: %s must be at most %d characters", ErrInvalidMemberLink, field, maxFederationIDRunes)
	default:
		return nil
	}
}

// federationColorOrder is NextFederatedColor's own tie-break order when
// every color is equally (least) used — declaration order, matching the
// const block in enums.go.
var federationColorOrder = []UserColor{ColorIndigo, ColorSteel, ColorTeal, ColorPeri}

// NextFederatedColor returns the least-used of the four owner palette
// colors across users, breaking ties by federationColorOrder's own
// declaration order. Pure and unit-testable: Nestova never pushes a color
// (NSTR-108), so Nestorage assigns its own on every federated create.
func NextFederatedColor(users []User) UserColor {
	counts := make(map[UserColor]int, len(federationColorOrder))
	for _, u := range users {
		counts[u.Color]++
	}
	best := federationColorOrder[0]
	bestCount := counts[best]
	for _, c := range federationColorOrder[1:] {
		if counts[c] < bestCount {
			best = c
			bestCount = counts[c]
		}
	}
	return best
}
