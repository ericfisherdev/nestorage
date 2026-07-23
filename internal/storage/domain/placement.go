package domain

import (
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// Placement holds an item's destination — exactly one of a bin or a
// holder, never both — the argument ItemRepository.Move accepts. NSTR-29's
// add/remove/return operations build their transitions with
// PlacementInBin/PlacementHeldBy rather than constructing this directly;
// the same exclusivity Item.Validate enforces on CurrentBinID/HeldBy, kept
// as its own type because Move takes a bare destination, not a whole Item.
type Placement struct {
	BinID  *BinID
	HeldBy *identity.UserID
}

// PlacementInBin returns the Placement for moving an item into bin.
func PlacementInBin(bin BinID) Placement { return Placement{BinID: &bin} }

// PlacementHeldBy returns the Placement for checking an item out to user.
func PlacementHeldBy(user identity.UserID) Placement { return Placement{HeldBy: &user} }

// Valid reports whether p holds exactly one of BinID/HeldBy — the Go-side
// mirror of the item_placement_exclusive database CHECK, checked again here
// (not only in Item.Validate) because Move accepts a bare Placement rather
// than a whole Item.
func (p Placement) Valid() bool {
	return (p.BinID != nil) != (p.HeldBy != nil)
}
