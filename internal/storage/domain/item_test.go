package domain_test

import (
	"errors"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

func TestValidateItemName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"blank rejected", "", domain.ErrItemNameRequired},
		{"whitespace-only rejected", "   \t  ", domain.ErrItemNameRequired},
		{"normal name accepted", "Camping stove", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := domain.ValidateItemName(tt.input); !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateItemName(%q) = %v, want %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidateItemQuantity(t *testing.T) {
	tests := []struct {
		name    string
		input   int
		wantErr error
	}{
		{"zero rejected", 0, domain.ErrInvalidQuantity},
		{"negative rejected", -1, domain.ErrInvalidQuantity},
		{"positive accepted", 1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := domain.ValidateItemQuantity(tt.input); !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateItemQuantity(%d) = %v, want %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// validItem returns an Item that passes Validate (in a bin, not held), so
// each Item_Validate subtest can mutate exactly one field away from valid
// and confirm that field alone is what Validate rejects.
func validItem() *domain.Item {
	bin := domain.NewBinID()
	return &domain.Item{
		ID:           domain.NewItemID(),
		Name:         "Camping stove",
		Quantity:     1,
		CurrentBinID: &bin,
		CreatedBy:    identity.NewUserID(),
	}
}

func TestItem_Validate(t *testing.T) {
	holder := identity.NewUserID()
	tests := []struct {
		name    string
		mutate  func(*domain.Item)
		wantErr error
	}{
		{"valid item in a bin accepted", func(*domain.Item) {}, nil},
		{"valid held item accepted", func(i *domain.Item) { i.CurrentBinID = nil; i.HeldBy = &holder }, nil},
		{"blank name rejected", func(i *domain.Item) { i.Name = "  " }, domain.ErrItemNameRequired},
		{"zero quantity rejected", func(i *domain.Item) { i.Quantity = 0 }, domain.ErrInvalidQuantity},
		{"negative quantity rejected", func(i *domain.Item) { i.Quantity = -3 }, domain.ErrInvalidQuantity},
		{"neither bin nor holder rejected", func(i *domain.Item) { i.CurrentBinID = nil }, domain.ErrInvalidPlacement},
		{"both bin and holder rejected", func(i *domain.Item) { i.HeldBy = &holder }, domain.ErrInvalidPlacement},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := validItem()
			tt.mutate(i)
			if err := i.Validate(); !errors.Is(err, tt.wantErr) {
				t.Errorf("Validate() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestItem_State(t *testing.T) {
	bin := domain.NewBinID()
	holder := identity.NewUserID()

	tests := []struct {
		name string
		item *domain.Item
		want domain.PlacementState
	}{
		{"in a bin", &domain.Item{CurrentBinID: &bin}, domain.StateInBin},
		{"checked out", &domain.Item{HeldBy: &holder}, domain.StateCheckedOut},
		{"neither set is undefined", &domain.Item{}, domain.PlacementState("")},
		{"both set is undefined", &domain.Item{CurrentBinID: &bin, HeldBy: &holder}, domain.PlacementState("")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.item.State(); got != tt.want {
				t.Errorf("State() = %q, want %q", got, tt.want)
			}
		})
	}
}

// sqlItemVisibilityMirror restates, in Go, the exact SQL WHERE fragment
// storage/adapter's ItemRepository uses to scope Get/ListByBin:
//
//	i.held_by IS NOT NULL OR b.visibility = 'public' OR b.created_by = $viewerID OR $viewerIsAdmin
//
// extending identity.CanSeeBin with the held-item exception: an item with no
// current bin has nothing to gate on and is always visible. Comparing this
// against CanSeeBin (for the in-bin case) across the full principal matrix
// is what keeps the hand-written SQL honest without needing a database for
// this check — the gated item_postgres_gated_test.go suite separately
// proves the SQL text itself behaves this way against a real Postgres.
func sqlItemVisibilityMirror(viewer identity.Principal, it *domain.Item, bin *domain.Bin) bool {
	if it.HeldBy != nil {
		return true
	}
	return identity.CanSeeBin(viewer, bin)
}

func TestItemSQLPredicateAgreesWithCanSeeBin(t *testing.T) {
	creator := identity.NewUserID()
	other := identity.NewUserID()
	binID := domain.NewBinID()

	principals := []identity.Principal{
		identity.NewUserPrincipal(other, identity.RoleAdmin, "Admin"),
		identity.NewUserPrincipal(creator, identity.RoleMember, "Creator"),
		identity.NewUserPrincipal(other, identity.RoleMember, "Non-creator member"),
		identity.NewIntegrationPrincipal("Nestova"),
		{}, // anonymous
	}
	visibilities := []domain.Visibility{domain.VisibilityPublic, domain.VisibilityPrivate}

	for _, visibility := range visibilities {
		bin := &domain.Bin{ID: binID, CreatedBy: creator, Visibility: visibility}
		inBinItem := &domain.Item{CurrentBinID: &binID}
		heldItem := &domain.Item{HeldBy: &creator}

		for _, p := range principals {
			if got, want := sqlItemVisibilityMirror(p, inBinItem, bin), identity.CanSeeBin(p, bin); got != want {
				t.Errorf("in-bin item, visibility=%s principal=%+v: mirror = %v, CanSeeBin = %v",
					visibility, p, got, want)
			}
			if !sqlItemVisibilityMirror(p, heldItem, bin) {
				t.Errorf("held item, visibility=%s principal=%+v: mirror = false, want true (held items are ungated)",
					visibility, p)
			}
		}
	}
}
