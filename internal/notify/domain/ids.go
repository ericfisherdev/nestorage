package domain

import (
	"fmt"

	"github.com/google/uuid"
)

// NotificationID uniquely identifies one notification row: one event
// delivered to one recipient over one channel (NSTR-44).
type NotificationID uuid.UUID

// NewNotificationID returns a new time-ordered (UUIDv7) notification id,
// mirroring identity.NewUserID's rationale: better B-tree index locality on
// insert than a random v4 id. uuid.NewV7 only errors if the crypto random
// source is unavailable — the same failure under which uuid.New itself
// panics — so Must is appropriate here.
func NewNotificationID() NotificationID { return NotificationID(uuid.Must(uuid.NewV7())) }

// String returns the canonical UUID string.
func (id NotificationID) String() string { return uuid.UUID(id).String() }

// ParseNotificationID parses a canonical UUID string into a NotificationID.
func ParseNotificationID(s string) (NotificationID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return NotificationID{}, fmt.Errorf("parse notification id: %w", err)
	}
	return NotificationID(u), nil
}
