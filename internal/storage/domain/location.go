package domain

import (
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// Location is an aggregate root for the storage bounded context: an area of
// the house that bins sit in ("Garage", "Hall closet"). ParentID is nil for
// a top-level location; the schema carries the self-reference from day one
// so nested areas can be enabled later without a migration against live
// data, even though nothing surfaces nesting yet.
//
// CreatedBy is identity.UserID — the storage context's first cross-context
// import, and a deliberate one: a value-object reference to another bounded
// context's aggregate identity is the honest DDD choice here, not a layering
// violation.
//
// Location is a plain struct, like identity.User: no logic lives on it
// directly. NewLocationID validated names, timestamps, and cross-aggregate
// invariants (e.g. "not empty") belong to the caller (app layer, once
// NSTR-29/30 add one) and the repository, not to this type.
type Location struct {
	ID          LocationID
	Name        string
	Description string
	ParentID    *LocationID
	CreatedBy   identity.UserID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
