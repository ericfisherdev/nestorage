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

	// ErrBinNotEmpty is returned by BinRepository.Delete when an item
	// (NSTR-28) still references the bin — enforced at the database by
	// item.current_bin_id's ON DELETE RESTRICT foreign key, the bin-side
	// analog of ErrLocationNotEmpty.
	ErrBinNotEmpty = errors.New("storage: bin is not empty")

	// ErrInvalidBin is returned (wrapped) by Bin.Validate for a malformed
	// bin.
	ErrInvalidBin = errors.New("storage: invalid bin")

	// ErrInvalidVisibility is returned (wrapped, with the offending value)
	// by ParseVisibility when given a string that is not a known
	// Visibility.
	ErrInvalidVisibility = errors.New("storage: invalid bin visibility")

	// ErrItemNotFound is returned by ItemRepository methods that look up or
	// mutate a specific item (Get, GetForUpdate, Update, Move, Delete) when
	// no matching row exists, or exists but is not visible to the
	// requesting viewer — the same "not found" masking ErrBinNotFound's own
	// doc requires, so a member cannot even confirm another member's
	// private-bin item exists.
	ErrItemNotFound = errors.New("storage: item not found")

	// ErrInvalidQuantity is returned (wrapped) by Item.Validate, and by
	// ItemRepository.Create/Update, for a quantity that is not strictly
	// positive — the domain mirror of the item_quantity_check database
	// CHECK.
	ErrInvalidQuantity = errors.New("storage: item quantity must be positive")

	// ErrInvalidPlacement is returned (wrapped) by Item.Validate, and by
	// ItemRepository.Create/Move, when an item does not have exactly one of
	// CurrentBinID/HeldBy set — the domain mirror of the
	// item_placement_exclusive database CHECK.
	ErrInvalidPlacement = errors.New("storage: item must be placed in exactly one bin or held by exactly one user")

	// ErrItemNameRequired is returned (wrapped) by Item.Validate for a
	// blank (or whitespace-only) name — the domain mirror of item's own
	// blank-name CHECK.
	ErrItemNameRequired = errors.New("storage: item name is required")

	// ErrItemAlreadyInBin is returned by Item.EnterBin when the item is
	// already sitting in a bin (see Item.InBin) — NSTR-29's
	// app.OperationService.AddToBin surfaces this so "add to bin" cannot
	// silently re-home an already-binned item; that is a move, a later
	// ticket's operation, not this one's.
	ErrItemAlreadyInBin = errors.New("storage: item is already in a bin")

	// ErrItemAlreadyCheckedOut is returned by Item.CheckOut when the item is
	// already held by a user (see Item.CheckedOut) — NSTR-29's
	// app.OperationService.RemoveFromBin surfaces this so a second,
	// lost-race check-out attempt on the same item fails cleanly instead of
	// silently overwriting the first holder.
	ErrItemAlreadyCheckedOut = errors.New("storage: item is already checked out")

	// ErrItemNotCheckedOut is returned by Item.ReturnTo when the item is not
	// currently held — NSTR-29's app.OperationService.ReturnToBin surfaces
	// this for a return attempt on an item that is already sitting in a
	// bin.
	ErrItemNotCheckedOut = errors.New("storage: item is not checked out")

	// ErrHolderRequired is returned by app.OperationService.RemoveFromBin
	// when the acting principal is not a real person (identity.KindUser) —
	// the Nestova integration's account api key principal has no person
	// behind it to attribute a checked-out item's hold to.
	ErrHolderRequired = errors.New("storage: only a user principal may check out an item")

	// ErrBinAlreadyInLocation is returned by Bin.MoveTo when the target
	// location equals the bin's current LocationID — NSTR-30's no-op guard,
	// kept in the domain (not app.BinMover) so it holds regardless of
	// caller, the same "guard in the domain" contract EnterBin/CheckOut/
	// ReturnTo follow for Item.
	ErrBinAlreadyInLocation = errors.New("storage: bin already in that location")
)
