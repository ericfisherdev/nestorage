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

	// ErrBinNotFound is returned by BinRepository methods that look up or
	// mutate a specific bin (FindVisibleByID, FindVisibleByCode,
	// UpdateVisibility, Delete) when no matching row exists, or exists but
	// is not visible/mutable to the requesting viewer — the same "not
	// found" masking CanSeeBin's own doc requires, so a member cannot even
	// confirm another member's private bin exists.
	ErrBinNotFound = errors.New("storage: bin not found")

	// ErrDuplicateBinCode is returned by BinRepository.Create when code is
	// already assigned to another bin — enforced by the bin_code_uniq
	// unique constraint.
	ErrDuplicateBinCode = errors.New("storage: bin code already in use")

	// ErrInvalidBin is returned (wrapped) by Bin.Validate for a malformed
	// bin.
	ErrInvalidBin = errors.New("storage: invalid bin")

	// ErrInvalidVisibility is returned (wrapped, with the offending value)
	// by ParseVisibility when given a string that is not a known
	// Visibility.
	ErrInvalidVisibility = errors.New("storage: invalid bin visibility")
)
