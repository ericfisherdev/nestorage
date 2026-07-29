package domain

import (
	"fmt"

	"github.com/google/uuid"
)

// UserID uniquely identifies a user.
type UserID uuid.UUID

// NewUserID returns a new time-ordered (UUIDv7) user id, which gives better
// B-tree index locality than random v4 ids. uuid.NewV7 only errors if the
// crypto random source is unavailable — the same failure under which
// uuid.New itself panics — so Must is appropriate here.
func NewUserID() UserID { return UserID(uuid.Must(uuid.NewV7())) }

// String returns the canonical UUID string.
func (id UserID) String() string { return uuid.UUID(id).String() }

// ParseUserID parses a canonical UUID string into a UserID.
func ParseUserID(s string) (UserID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return UserID{}, fmt.Errorf("parse user id: %w", err)
	}
	return UserID(u), nil
}

// APIKeyID uniquely identifies the account's api key (NSTR-23).
type APIKeyID uuid.UUID

// NewAPIKeyID returns a new time-ordered (UUIDv7) api key id, the same
// B-tree-locality rationale as NewUserID.
func NewAPIKeyID() APIKeyID { return APIKeyID(uuid.Must(uuid.NewV7())) }

// String returns the canonical UUID string.
func (id APIKeyID) String() string { return uuid.UUID(id).String() }

// ParseAPIKeyID parses a canonical UUID string into an APIKeyID.
func ParseAPIKeyID(s string) (APIKeyID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return APIKeyID{}, fmt.Errorf("parse api key id: %w", err)
	}
	return APIKeyID(u), nil
}
