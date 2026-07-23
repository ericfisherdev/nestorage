package components_test

import (
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/web/components"
)

func TestItemDetail_InBin_RendersBinAndCheckOutControl(t *testing.T) {
	view := components.ItemDetailView{
		CSRFToken: "tok", ID: "item-1", Name: "Camping stove", Description: "Two-burner", Quantity: 2,
		InBin: true, BinCode: "BIN-A01", LocationName: "Garage",
	}
	out := renderString(t, components.ItemDetail(view))

	if !strings.Contains(out, "Camping stove") || !strings.Contains(out, "Two-burner") {
		t.Errorf("ItemDetail() missing name/description: %s", out)
	}
	if !strings.Contains(out, "Quantity: 2") {
		t.Errorf("ItemDetail() missing quantity: %s", out)
	}
	if !strings.Contains(out, "BIN-A01") || !strings.Contains(out, "Garage") {
		t.Errorf("ItemDetail() missing bin code/location: %s", out)
	}
	if !strings.Contains(out, "Check out") {
		t.Errorf("ItemDetail() in-bin item missing the check-out control: %s", out)
	}
	if strings.Contains(out, "Return to bin") {
		t.Errorf("ItemDetail() in-bin item should not render the return control: %s", out)
	}
	if !strings.Contains(out, `id="item-detail"`) {
		t.Error("ItemDetail() missing its own id, which the operation controls' hx-target relies on")
	}
}

func TestItemDetail_CheckedOut_RendersHolderAndSinceWhenAndReturnControl(t *testing.T) {
	view := components.ItemDetailView{
		CSRFToken: "tok", ID: "item-1", Name: "Sleeping bag", Quantity: 1, InBin: false,
		Holder:     components.OwnerView{Name: "Maya", Initials: "M", Color: components.OwnerIndigo},
		HeldSince:  "Jul 20, 2026",
		ReturnBins: []components.BinOptionView{{ID: "bin-1", Label: "BIN-A01 — Winter Clothes"}},
	}
	out := renderString(t, components.ItemDetail(view))

	if !strings.Contains(out, "Maya") {
		t.Errorf("ItemDetail() missing the holder's name: %s", out)
	}
	if !strings.Contains(out, "has held this since Jul 20, 2026.") {
		t.Errorf("ItemDetail() missing the plain held-since sentence (the AC's headline): %s", out)
	}
	if !strings.Contains(out, "Return to bin") {
		t.Errorf("ItemDetail() checked-out item missing the return control: %s", out)
	}
	if strings.Contains(out, "Check out") {
		t.Errorf("ItemDetail() checked-out item should not render the check-out control: %s", out)
	}
	if !strings.Contains(out, `value="bin-1"`) || !strings.Contains(out, "BIN-A01 — Winter Clothes") {
		t.Errorf("ItemDetail() missing the return control's bin option: %s", out)
	}
}

func TestItemDetail_FormError_Rendered(t *testing.T) {
	view := components.ItemDetailView{ID: "item-1", Name: "Stove", InBin: true, FormError: "This item is already checked out."}
	out := renderString(t, components.ItemDetail(view))

	if !strings.Contains(out, "This item is already checked out.") {
		t.Errorf("ItemDetail() missing FormError: %s", out)
	}
	if !strings.Contains(out, `role="alert"`) {
		t.Errorf("ItemDetail() FormError missing role=alert: %s", out)
	}
}

func TestItemDetail_NoDescription_OmitsDescriptionParagraph(t *testing.T) {
	view := components.ItemDetailView{ID: "item-1", Name: "Stove", InBin: true}
	out := renderString(t, components.ItemDetail(view))

	if strings.Contains(out, "mt-2 text-sm text-text-secondary") {
		t.Errorf("ItemDetail() rendered a description paragraph for a blank description: %s", out)
	}
}
