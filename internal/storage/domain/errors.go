package domain

import "errors"

// Domain errors for the storage bounded context. NSTR-27 (bins) and NSTR-28
// (items) append their own sentinels here rather than starting a second
// errors.go — one context, one error file.
var (
	// ErrLocationNotFound is returned by LocationRepository methods that look
	// up or mutate a specific location (FindByID, Rename, Delete) when no
	// matching row exists.
	ErrLocationNotFound = errors.New("storage: location not found")

	// ErrLocationNotEmpty is returned by LocationRepository.Delete when a
	// dependent row (today: a child location; later: a bin from NSTR-27)
	// still references the location — enforced at the database by
	// parent_id's ON DELETE RESTRICT foreign key.
	ErrLocationNotEmpty = errors.New("storage: location is not empty")

	// ErrInvalidLocationName is returned (wrapped) by ValidateLocationName
	// for a blank, whitespace-only, or over-long name.
	ErrInvalidLocationName = errors.New("storage: invalid location name")
)
