package domain

import (
	"context"
	"strings"
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// ValidateItemName reports whether name is well-formed: not blank (including
// not whitespace-only), wrapping ErrItemNameRequired. Unlike
// ValidateLocationName, it does not return a trimmed value — item names are
// stored exactly as typed, the same "checked, not normalized" contract
// Bin.Name follows. Exported (rather than inlined into Item.Validate only)
// so app.ItemService.Edit can apply the same rule to a name-only edit
// without constructing (and invalidly placement-checking) a whole Item.
func ValidateItemName(name string) error {
	if strings.TrimSpace(name) == "" {
		return ErrItemNameRequired
	}
	return nil
}

// ValidateItemQuantity reports whether quantity is well-formed (strictly
// positive), wrapping ErrInvalidQuantity — the domain mirror of the
// item_quantity_check database CHECK. Exported for the same reason as
// ValidateItemName: reused by both Item.Validate and app.ItemService.Edit.
func ValidateItemQuantity(quantity int) error {
	if quantity <= 0 {
		return ErrInvalidQuantity
	}
	return nil
}

// Item is an aggregate root for the storage bounded context: a named,
// counted thing sitting in exactly one Bin or checked out to exactly one
// person — never both, never neither. Removing an item from a bin does not
// delete it; the item becomes held (see ItemRepository.Move), so
// CurrentBinID and HeldBy stay mutually exclusive for the item's entire
// lifetime, not only at creation.
//
// PlacementChangedAt tracks when CurrentBinID/HeldBy last changed — set to
// the row's creation time on insert, and advanced by NSTR-29 on every
// add/remove/return, never by a plain Update (see ItemRepository.Update's
// own doc). NSTR-32 reads it for "held/placed since" display.
//
// Item is a plain struct, like Bin and Location: no logic beyond Validate
// and State lives on it directly.
type Item struct {
	ID                 ItemID
	Name               string
	Description        *string
	Quantity           int
	CurrentBinID       *BinID
	HeldBy             *identity.UserID
	CreatedBy          identity.UserID
	PlacementChangedAt time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Validate reports whether the item is well-formed: ErrItemNameRequired for
// a blank name, ErrInvalidQuantity for a quantity that is not strictly
// positive, and ErrInvalidPlacement when CurrentBinID/HeldBy is not exactly
// one-set — the domain mirror of item's database CHECKs, run before Create
// ever reaches the database.
func (i *Item) Validate() error {
	if err := ValidateItemName(i.Name); err != nil {
		return err
	}
	if err := ValidateItemQuantity(i.Quantity); err != nil {
		return err
	}
	if !(Placement{BinID: i.CurrentBinID, HeldBy: i.HeldBy}).Valid() {
		return ErrInvalidPlacement
	}
	return nil
}

// State reports whether i is currently sitting in a bin or checked out to a
// person, derived from whichever of CurrentBinID/HeldBy is set — see
// PlacementState's own doc for why this is computed rather than stored.
// Callers should validate i first: State returns the zero PlacementState
// ("") for an Item that fails Validate (neither or both set).
func (i *Item) State() PlacementState {
	switch {
	case i.HeldBy != nil && i.CurrentBinID == nil:
		return StateCheckedOut
	case i.CurrentBinID != nil && i.HeldBy == nil:
		return StateInBin
	default:
		return ""
	}
}

// ItemRepository is the outbound port for persisting and retrieving items,
// scoped throughout (on its read paths) by a viewer identity.Principal so
// visibility is enforced in the query itself rather than as an
// after-the-fact filter — the same approach BinRepository takes.
// Implementations live in the adapter package.
//
// A held item (HeldBy set, CurrentBinID nil) has no bin to inherit privacy
// from and is treated as ungated: every viewer can see a checked-out item
// regardless of which bin it last sat in. This is a deliberate sprint-level
// decision, not a gap — see the migration's own comment.
//
// Persistence contracts (the caller sets identity and valid fields; the
// store sets timestamps):
//   - Create expects it.ID, a validated it.Name/it.Quantity (see
//     ValidateItemName/ValidateItemQuantity and Item.Validate), exactly one
//     of it.CurrentBinID/it.HeldBy set, and a valid it.CreatedBy; it
//     populates PlacementChangedAt/CreatedAt/UpdatedAt (all three read the
//     same insert-time now(), see the migration's own comment).
//   - Update overwrites only name, description, and quantity — never
//     placement or creator; placement changes go through Move, never
//     through Update, so Update never touches PlacementChangedAt.
//
// Error contracts:
//   - Create returns ErrInvalidQuantity when quantity is not strictly
//     positive, ErrInvalidPlacement when CurrentBinID/HeldBy is not exactly
//     one-set, ErrBinNotFound when current_bin_id is unknown, or
//     identity.ErrUserNotFound when held_by or created_by is unknown —
//     each mapped from the database CHECK/foreign-key that enforces it, not
//     only from Item.Validate.
//   - Get, GetForUpdate, Update, Move, and Delete return ErrItemNotFound
//     when id is unknown; Get additionally returns it when the item exists
//     but is not visible to viewer (CanSeeBin, extended for the held-item
//     exception above), so a member cannot even confirm another member's
//     private-bin item exists.
//   - Update also returns ErrInvalidQuantity/ErrItemNameRequired when the
//     new values violate the database CHECKs.
//   - Move returns ErrInvalidPlacement for a malformed dst (Placement.Valid
//     is false) or a database CHECK violation, ErrBinNotFound when dst's
//     bin is unknown, or identity.ErrUserNotFound when dst's holder is
//     unknown.
//   - ListByBin returns every item in binID viewer may see, empty slice
//     when none.
//   - Delete returns ErrItemNotFound when id is unknown. It is not
//     visibility-scoped: the app layer is responsible for authorizing a
//     delete (typically via a preceding Get) before calling it.
type ItemRepository interface {
	Create(ctx context.Context, it *Item) error
	// Get returns the item, scoped to what viewer may see.
	Get(ctx context.Context, viewer identity.Principal, id ItemID) (*Item, error)
	// GetForUpdate returns the item locked FOR UPDATE within the caller's
	// transaction — the caller must supply a pgx.Tx (via the repository's
	// constructor) for the lock to have any scope beyond the single
	// statement. Not visibility-scoped: NSTR-29 calls this only after a
	// prior Get has already confirmed the principal may see the item.
	GetForUpdate(ctx context.Context, id ItemID) (*Item, error)
	Update(ctx context.Context, it *Item) error
	// Move is the placement primitive NSTR-29's add/remove/return build on:
	// it swaps current_bin_id/held_by to match dst in one statement,
	// advancing PlacementChangedAt, and reports the number of rows affected
	// (0 or 1) alongside the usual error contract, so a caller that already
	// holds the row FOR UPDATE can confirm the write actually landed
	// without a second round trip.
	Move(ctx context.Context, id ItemID, dst Placement) (rowsAffected int64, err error)
	// ListByBin returns every item in binID viewer may see, ordered by
	// name, tie-broken by id.
	ListByBin(ctx context.Context, viewer identity.Principal, binID BinID) ([]Item, error)
	// CountsByBin returns how many items viewer may see are currently sitting
	// in each bin, keyed by bin id — the aggregate the bin grid and location
	// detail pages need to show "N items" on every card without an N+1
	// ListByBin call per bin. A bin holding zero visible items is simply
	// absent from the map; the caller treats a missing key as zero, the same
	// "absence means zero/none" contract ListVisible's empty slice follows.
	CountsByBin(ctx context.Context, viewer identity.Principal) (map[BinID]int, error)
	Delete(ctx context.Context, id ItemID) error
	// FindVisibleDetail returns id's detail read model — the joined bin/
	// location name or holder name/color Get's bare Item does not carry —
	// scoped to what viewer may see (the same rule Get applies, including
	// the held-item exception in itemVisibilityWhere). Returns
	// ErrItemNotFound both when id is unknown and when the item exists but
	// is not visible to viewer.
	FindVisibleDetail(ctx context.Context, viewer identity.Principal, id ItemID) (*ItemDetailResult, error)
	// SearchVisible returns every item viewer may see whose own name/
	// description, its bin's name, or its location's name contains query
	// (case-insensitive substring, pg_trgm-accelerated — see
	// 00009_item_search.sql), ordered by name and tie-broken by id, capped
	// at limit rows. A held item matches only on its own name/description,
	// since it has no bin/location to join (see ItemSearchResult's own
	// doc). Returns an empty slice, not an error, when nothing matches.
	SearchVisible(ctx context.Context, viewer identity.Principal, query string, limit int) ([]ItemSearchResult, error)
}
