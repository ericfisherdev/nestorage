package domain_test

import (
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
