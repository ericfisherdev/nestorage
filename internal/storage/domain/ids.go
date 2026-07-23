package domain

import (
	"fmt"

	"github.com/google/uuid"
)

// LocationID uniquely identifies a storage location.
type LocationID uuid.UUID

// NewLocationID returns a new time-ordered (UUIDv7) location id, which gives
// better B-tree index locality than random v4 ids — the same rationale as
// identity.NewUserID. uuid.NewV7 only errors if the crypto random source is
// unavailable — the same failure under which uuid.New itself panics — so
// Must is appropriate here.
func NewLocationID() LocationID { return LocationID(uuid.Must(uuid.NewV7())) }

// String returns the canonical UUID string.
func (id LocationID) String() string { return uuid.UUID(id).String() }

// ParseLocationID parses a canonical UUID string into a LocationID.
func ParseLocationID(s string) (LocationID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return LocationID{}, fmt.Errorf("parse location id: %w", err)
	}
	return LocationID(u), nil
}

// ItemID uniquely identifies an item.
type ItemID uuid.UUID

// NewItemID returns a new time-ordered (UUIDv7) item id, mirroring
// NewBinID's rationale: better B-tree index locality than a random v4 id.
func NewItemID() ItemID { return ItemID(uuid.Must(uuid.NewV7())) }

// String returns the canonical UUID string.
func (id ItemID) String() string { return uuid.UUID(id).String() }

// ParseItemID parses a canonical UUID string into an ItemID.
func ParseItemID(s string) (ItemID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return ItemID{}, fmt.Errorf("parse item id: %w", err)
	}
	return ItemID(u), nil
}
