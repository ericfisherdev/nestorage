package domain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// BinID uniquely identifies a bin.
type BinID uuid.UUID

// NewBinID returns a new time-ordered (UUIDv7) bin id, mirroring
// NewLocationID's rationale: better B-tree index locality than a random v4
// id.
func NewBinID() BinID { return BinID(uuid.Must(uuid.NewV7())) }

// String returns the canonical UUID string.
func (id BinID) String() string { return uuid.UUID(id).String() }

// ParseBinID parses a canonical UUID string into a BinID.
func ParseBinID(s string) (BinID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return BinID{}, fmt.Errorf("parse bin id: %w", err)
	}
	return BinID(u), nil
}

// maxBinCodeRunes is the longest label code validateBinCode accepts, counted
// by rune (not byte) so a multi-byte character counts once, not per byte —
// the same reasoning as storage's maxLocationNameRunes.
const maxBinCodeRunes = 32

// NormalizeBinCode trims surrounding whitespace and upper-cases code, so a
// scanned label and a typed one always resolve to the same bin regardless of
// case — the one place this rule is defined, called by every write and
// lookup path. BinRepository does not re-normalize on write; the caller is
// responsible for calling this first (Create), and FindVisibleByCode calls
// it on the incoming lookup key since a scanned code's case cannot be
// trusted.
func NormalizeBinCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// validateBinCode reports whether code is well-formed: non-blank (checked
// via a trimmed comparison, the same way Bin.Validate checks Name, without
// mutating code itself) and at most maxBinCodeRunes.
func validateBinCode(code string) error {
	if strings.TrimSpace(code) == "" {
		return fmt.Errorf("%w: code must not be blank", ErrInvalidBin)
	}
	if len([]rune(code)) > maxBinCodeRunes {
		return fmt.Errorf("%w: code exceeds %d characters", ErrInvalidBin, maxBinCodeRunes)
	}
	return nil
}

// Bin is an aggregate root for the storage bounded context: a labeled
// container living at a Location. OwnerID is nil for the shared/Family bin
// and set to the household member whose color it wears in the browse UI
// otherwise. CreatedBy is the pivot for private Visibility and for item
// history attribution — always set, unlike OwnerID.
//
// Bin is a plain struct, like Location: no logic beyond Validate and the
// identity.BinSubject accessors lives on it directly. Code normalization
// (NormalizeBinCode) and validation belong to the caller before Create, the
// same contract ValidateLocationName documents for Location.Name.
type Bin struct {
	ID          BinID
	Code        string
	Name        string
	Description string
	LocationID  LocationID
	OwnerID     *identity.UserID
	CreatedBy   identity.UserID
	Visibility  Visibility
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Validate reports whether the bin is well-formed, wrapping ErrInvalidBin.
// It does not normalize Code — the caller calls NormalizeBinCode first, the
// same order ValidateLocationName's callers already follow for Name.
func (b *Bin) Validate() error {
	if b.ID == (BinID{}) {
		return fmt.Errorf("%w: id is required", ErrInvalidBin)
	}
	if err := validateBinCode(b.Code); err != nil {
		return err
	}
	if strings.TrimSpace(b.Name) == "" {
		return fmt.Errorf("%w: name must not be blank", ErrInvalidBin)
	}
	if b.LocationID == (LocationID{}) {
		return fmt.Errorf("%w: location id is required", ErrInvalidBin)
	}
	if b.CreatedBy == (identity.UserID{}) {
		return fmt.Errorf("%w: created by is required", ErrInvalidBin)
	}
	if !b.Visibility.Valid() {
		return fmt.Errorf("%w: invalid visibility %q", ErrInvalidBin, b.Visibility)
	}
	return nil
}

// BinCreator returns the id of the user who created the bin, satisfying
// identity.BinSubject.
func (b *Bin) BinCreator() identity.UserID { return b.CreatedBy }

// BinPrivate reports whether the bin is visible only to its creator and
// admins, satisfying identity.BinSubject.
func (b *Bin) BinPrivate() bool { return b.Visibility.IsPrivate() }

// Compile-time assurance that Bin satisfies identity's authorization view of
// it. The dependency points storage to identity only, never back — see
// identity/domain.BinSubject's own doc.
var _ identity.BinSubject = (*Bin)(nil)

// BinRepository is the outbound port for persisting and retrieving bins,
// scoped throughout by a viewer identity.Principal so visibility is enforced
// in the query itself rather than as an after-the-fact filter. Implementations
// live in the adapter package.
//
// Persistence contracts (the caller sets identity and valid fields; the
// store sets timestamps and defaults):
//   - Create expects b.ID, a normalized+validated b.Code (see
//     NormalizeBinCode and Bin.Validate), b.Name, b.LocationID, an optional
//     b.OwnerID, and a valid b.CreatedBy; it populates CreatedAt/UpdatedAt.
//     An unset b.Visibility defaults to VisibilityPublic and is written back
//     onto b, matching the "bins default to public" acceptance criterion.
//
// Error contracts:
//   - Create returns ErrDuplicateBinCode when code is already in use
//     (bin_code_uniq), ErrLocationNotFound when location_id is unknown, or
//     identity.ErrUserNotFound when owner_id or created_by is unknown.
//   - FindVisibleByID and FindVisibleByCode return ErrBinNotFound both when
//     the row is missing and when it exists but viewer may not see it
//     (CanSeeBin), so a member cannot even confirm another member's private
//     bin exists.
//   - ListVisible returns every bin viewer may see, empty slice when none.
//   - UpdateVisibility and Delete return ErrBinNotFound when the row is
//     missing or viewer may not mutate it (CanMutateBin).
type BinRepository interface {
	Create(ctx context.Context, b *Bin) error
	FindVisibleByID(ctx context.Context, viewer identity.Principal, id BinID) (*Bin, error)
	FindVisibleByCode(ctx context.Context, viewer identity.Principal, code string) (*Bin, error)
	// ListVisible returns every bin viewer may see, ordered by code, tie-
	// broken by nothing further since code is unique.
	ListVisible(ctx context.Context, viewer identity.Principal) ([]Bin, error)
	UpdateVisibility(ctx context.Context, viewer identity.Principal, id BinID, visibility Visibility) error
	Delete(ctx context.Context, viewer identity.Principal, id BinID) error
}
