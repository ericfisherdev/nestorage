package domain_test

import (
	"errors"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

func TestPlacement_Valid(t *testing.T) {
	bin := domain.NewBinID()
	holder := identity.NewUserID()

	tests := []struct {
		name string
		p    domain.Placement
		want bool
	}{
		{"bin only is valid", domain.PlacementInBin(bin), true},
		{"holder only is valid", domain.PlacementHeldBy(holder), true},
		{"neither is invalid", domain.Placement{}, false},
		{"both is invalid", domain.Placement{BinID: &bin, HeldBy: &holder}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.Valid(); got != tt.want {
				t.Errorf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlacementInBin_RoundTrips(t *testing.T) {
	bin := domain.NewBinID()
	p := domain.PlacementInBin(bin)
	if p.BinID == nil || *p.BinID != bin {
		t.Errorf("PlacementInBin(%v).BinID = %v, want %v", bin, p.BinID, bin)
	}
	if p.HeldBy != nil {
		t.Errorf("PlacementInBin(%v).HeldBy = %v, want nil", bin, p.HeldBy)
	}
}

func TestPlacementHeldBy_RoundTrips(t *testing.T) {
	holder := identity.NewUserID()
	p := domain.PlacementHeldBy(holder)
	if p.HeldBy == nil || *p.HeldBy != holder {
		t.Errorf("PlacementHeldBy(%v).HeldBy = %v, want %v", holder, p.HeldBy, holder)
	}
	if p.BinID != nil {
		t.Errorf("PlacementHeldBy(%v).BinID = %v, want nil", holder, p.BinID)
	}
}

// binnedItem returns an Item sitting in a bin (InBin true, CheckedOut
// false), the starting state EnterBin must reject and CheckOut/ReturnTo
// must accept transitioning away from.
func binnedItem() *domain.Item {
	bin := domain.NewBinID()
	return &domain.Item{ID: domain.NewItemID(), Name: "Stove", Quantity: 1, CurrentBinID: &bin}
}

// heldItem returns an Item checked out to a user (CheckedOut true, InBin
// false), the starting state CheckOut must reject and EnterBin/ReturnTo
// must accept transitioning away from.
func heldItem() *domain.Item {
	holder := identity.NewUserID()
	return &domain.Item{ID: domain.NewItemID(), Name: "Stove", Quantity: 1, HeldBy: &holder}
}

func TestItem_InBinAndCheckedOut(t *testing.T) {
	binned, held := binnedItem(), heldItem()

	if !binned.InBin() || binned.CheckedOut() {
		t.Errorf("binned item: InBin() = %v, CheckedOut() = %v, want true, false", binned.InBin(), binned.CheckedOut())
	}
	if held.InBin() || !held.CheckedOut() {
		t.Errorf("held item: InBin() = %v, CheckedOut() = %v, want false, true", held.InBin(), held.CheckedOut())
	}
}

func TestItem_EnterBin(t *testing.T) {
	t.Run("from checked out succeeds", func(t *testing.T) {
		it := heldItem()
		dst := domain.NewBinID()
		if err := it.EnterBin(dst); err != nil {
			t.Fatalf("EnterBin: %v", err)
		}
		if it.CurrentBinID == nil || *it.CurrentBinID != dst {
			t.Errorf("EnterBin: CurrentBinID = %v, want %v", it.CurrentBinID, dst)
		}
		if it.HeldBy != nil {
			t.Error("EnterBin must clear HeldBy")
		}
	})

	t.Run("already in a bin rejected, unmodified", func(t *testing.T) {
		it := binnedItem()
		original := *it.CurrentBinID
		dst := domain.NewBinID()

		if err := it.EnterBin(dst); !errors.Is(err, domain.ErrItemAlreadyInBin) {
			t.Errorf("EnterBin(already in bin) = %v, want ErrItemAlreadyInBin", err)
		}
		if it.CurrentBinID == nil || *it.CurrentBinID != original {
			t.Error("rejected EnterBin must leave CurrentBinID unmodified")
		}
	})
}

func TestItem_CheckOut(t *testing.T) {
	t.Run("from in a bin succeeds", func(t *testing.T) {
		it := binnedItem()
		holder := identity.NewUserID()
		if err := it.CheckOut(holder); err != nil {
			t.Fatalf("CheckOut: %v", err)
		}
		if it.HeldBy == nil || *it.HeldBy != holder {
			t.Errorf("CheckOut: HeldBy = %v, want %v", it.HeldBy, holder)
		}
		if it.CurrentBinID != nil {
			t.Error("CheckOut must clear CurrentBinID")
		}
	})

	t.Run("already checked out rejected, unmodified", func(t *testing.T) {
		it := heldItem()
		original := *it.HeldBy
		newHolder := identity.NewUserID()

		if err := it.CheckOut(newHolder); !errors.Is(err, domain.ErrItemAlreadyCheckedOut) {
			t.Errorf("CheckOut(already checked out) = %v, want ErrItemAlreadyCheckedOut", err)
		}
		if it.HeldBy == nil || *it.HeldBy != original {
			t.Error("rejected CheckOut must leave HeldBy unmodified")
		}
	})
}

func TestItem_ReturnTo(t *testing.T) {
	t.Run("from checked out succeeds", func(t *testing.T) {
		it := heldItem()
		dst := domain.NewBinID()
		if err := it.ReturnTo(dst); err != nil {
			t.Fatalf("ReturnTo: %v", err)
		}
		if it.CurrentBinID == nil || *it.CurrentBinID != dst {
			t.Errorf("ReturnTo: CurrentBinID = %v, want %v", it.CurrentBinID, dst)
		}
		if it.HeldBy != nil {
			t.Error("ReturnTo must clear HeldBy")
		}
	})

	t.Run("not checked out rejected, unmodified", func(t *testing.T) {
		it := binnedItem()
		original := *it.CurrentBinID
		dst := domain.NewBinID()

		if err := it.ReturnTo(dst); !errors.Is(err, domain.ErrItemNotCheckedOut) {
			t.Errorf("ReturnTo(not checked out) = %v, want ErrItemNotCheckedOut", err)
		}
		if it.CurrentBinID == nil || *it.CurrentBinID != original {
			t.Error("rejected ReturnTo must leave CurrentBinID unmodified")
		}
	})
}
