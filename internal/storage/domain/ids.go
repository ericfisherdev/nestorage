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

// ItemEventID uniquely identifies an entry in the append-only item event log
// (NSTR-40).
type ItemEventID uuid.UUID

// NewItemEventID returns a new time-ordered (UUIDv7) item event id, mirroring
// NewItemID's rationale: better B-tree index locality than a random v4 id.
func NewItemEventID() ItemEventID { return ItemEventID(uuid.Must(uuid.NewV7())) }

// String returns the canonical UUID string.
func (id ItemEventID) String() string { return uuid.UUID(id).String() }

// ParseItemEventID parses a canonical UUID string into an ItemEventID.
func ParseItemEventID(s string) (ItemEventID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return ItemEventID{}, fmt.Errorf("parse item event id: %w", err)
	}
	return ItemEventID(u), nil
}

// ReturnRequestID uniquely identifies a household member's ask for a
// checked-out item back (NSTR-43).
type ReturnRequestID uuid.UUID

// NewReturnRequestID returns a new time-ordered (UUIDv7) return request id,
// mirroring NewItemEventID's rationale: better B-tree index locality than a
// random v4 id.
func NewReturnRequestID() ReturnRequestID { return ReturnRequestID(uuid.Must(uuid.NewV7())) }

// String returns the canonical UUID string.
func (id ReturnRequestID) String() string { return uuid.UUID(id).String() }

// ParseReturnRequestID parses a canonical UUID string into a ReturnRequestID.
func ParseReturnRequestID(s string) (ReturnRequestID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return ReturnRequestID{}, fmt.Errorf("parse return request id: %w", err)
	}
	return ReturnRequestID(u), nil
}

// ItemLinkID uniquely identifies a labeled URL attached to an item.
type ItemLinkID uuid.UUID

// NewItemLinkID returns a new time-ordered (UUIDv7) item link id, mirroring
// NewItemID's rationale: better B-tree index locality than a random v4 id.
func NewItemLinkID() ItemLinkID { return ItemLinkID(uuid.Must(uuid.NewV7())) }

// String returns the canonical UUID string.
func (id ItemLinkID) String() string { return uuid.UUID(id).String() }

// ParseItemLinkID parses a canonical UUID string into an ItemLinkID.
func ParseItemLinkID(s string) (ItemLinkID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return ItemLinkID{}, fmt.Errorf("parse item link id: %w", err)
	}
	return ItemLinkID(u), nil
}
