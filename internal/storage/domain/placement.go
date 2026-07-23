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

// InBin reports whether i is currently sitting in a bin (not held),
// equivalent to i.State() == StateInBin. A dedicated predicate (rather than
// every caller comparing State() itself) is what keeps
// EnterBin/CheckOut/ReturnTo's own guards readable.
func (i *Item) InBin() bool { return i.State() == StateInBin }

// CheckedOut reports whether i is currently held by a user (not in a bin),
// equivalent to i.State() == StateCheckedOut.
func (i *Item) CheckedOut() bool { return i.State() == StateCheckedOut }

// EnterBin transitions i into binID, clearing any holder — the "add to bin"
// primitive app.OperationService.AddToBin builds on. Both placement fields
// are set together so a caller reading i mid-transition never sees the
// "neither set" state the item_placement_exclusive CHECK forbids. Returns
// ErrItemAlreadyInBin, leaving i unmodified, if i is already sitting in a
// bin — moving an already-binned item to a different bin is a later
// ticket's move operation, not this one.
func (i *Item) EnterBin(binID BinID) error {
	if i.InBin() {
		return ErrItemAlreadyInBin
	}
	i.CurrentBinID, i.HeldBy = &binID, nil
	return nil
}

// CheckOut transitions i to being held by holder, clearing its bin — the
// "remove from bin" primitive app.OperationService.RemoveFromBin builds on.
// Returns ErrItemAlreadyCheckedOut, leaving i unmodified, if i is already
// held — the guard that fails a lost-race concurrent check-out attempt (see
// OperationService's own transactional doc).
func (i *Item) CheckOut(holder identity.UserID) error {
	if i.CheckedOut() {
		return ErrItemAlreadyCheckedOut
	}
	i.HeldBy, i.CurrentBinID = &holder, nil
	return nil
}

// ReturnTo transitions i back into binID, clearing its holder — the
// "return to bin" primitive app.OperationService.ReturnToBin builds on.
// Returns ErrItemNotCheckedOut, leaving i unmodified, if i is not currently
// held.
func (i *Item) ReturnTo(binID BinID) error {
	if !i.CheckedOut() {
		return ErrItemNotCheckedOut
	}
	i.CurrentBinID, i.HeldBy = &binID, nil
	return nil
}
