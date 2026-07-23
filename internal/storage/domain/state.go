package domain

// PlacementState is the derived state of an item: sitting in a bin, or
// checked out to a person. There is no stored status column — CurrentBinID
// and HeldBy are the single source of truth, and Item.State computes this
// from whichever is set, the same reasoning identity.DeviceToken.Active
// applies deriving its state from revoked_at rather than storing a
// redundant flag that could drift out of sync.
type PlacementState string

// The two placement states an Item.Validate-passing item can be in.
const (
	StateInBin      PlacementState = "in_bin"
	StateCheckedOut PlacementState = "checked_out"
)

// String returns the state's display value.
func (s PlacementState) String() string { return string(s) }
