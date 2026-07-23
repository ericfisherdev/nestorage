package app

import (
	"context"
	"strings"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// memberLister is the narrow port (ISP) BinService/LocationService depend
// on to enrich a bin's OwnerID into a display name/initials/color, so the
// web layer never reaches into the identity repository directly (NSTR-31's
// sprint-level decision) — satisfied by identity/domain.UserRepository (a
// superset, via List) and by test fakes. Named for the single method it
// exposes, per Go's single-method-interface naming convention (io.Reader,
// fmt.Stringer, ...) — mirrors app.binFinder's own naming rationale
// (operations.go).
type memberLister interface {
	List(ctx context.Context) ([]identity.User, error)
}

// OwnerInfo carries a bin owner's display name, initials, and color — the
// projection the web adapter renders as an OwnerAvatar without ever loading
// an identity.User itself. A nil *OwnerInfo (see BinView.Owner) means the
// bin is unowned — the shared/Family bin the web layer renders as
// components.OwnerShared.
type OwnerInfo struct {
	UserID   identity.UserID
	Name     string
	Initials string
	Color    identity.UserColor
}

// memberIndex maps a household's members by id, built once per request so
// BinService can enrich every bin in a ListVisible/ListVisibleByLocation
// result without querying identity once per bin.
type memberIndex map[identity.UserID]identity.User

// newMemberIndex loads every household member via dir and indexes them by
// id.
func newMemberIndex(ctx context.Context, dir memberLister) (memberIndex, error) {
	members, err := dir.List(ctx)
	if err != nil {
		return nil, err
	}
	idx := make(memberIndex, len(members))
	for _, m := range members {
		idx[m.ID] = m
	}
	return idx, nil
}

// ownerInfo projects ownerID (nil for the shared/Family bin) into an
// *OwnerInfo via idx, falling back to nil for an owner id idx has no member
// for (a member deleted out from under a bin they used to own — this
// service degrades to "unowned" rather than failing the whole read).
func (idx memberIndex) ownerInfo(ownerID *identity.UserID) *OwnerInfo {
	if ownerID == nil {
		return nil
	}
	m, ok := idx[*ownerID]
	if !ok {
		return nil
	}
	return &OwnerInfo{UserID: m.ID, Name: m.DisplayName, Initials: initials(m.DisplayName), Color: m.Color}
}

// initials returns the first letter of name, uppercased, matching the
// identity web adapter's own initials helper (users_web.go) so an
// OwnerInfo's initial always agrees with the admin user-management
// screen's. A rune slice is used so a multi-byte first character is not
// split.
func initials(name string) string {
	r := []rune(strings.TrimSpace(name))
	if len(r) == 0 {
		return "?"
	}
	return strings.ToUpper(string(r[0]))
}
