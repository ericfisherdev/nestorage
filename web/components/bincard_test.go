package components_test

import (
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/web/components"
)

func testOwnerView() components.OwnerView {
	return components.OwnerView{Name: "Maya", Initials: "M", Color: components.OwnerIndigo}
}

func testBinCardView() components.BinCardView {
	return components.BinCardView{
		Code: "BIN-A01", Name: "Winter Clothes", LocationName: "Garage",
		ItemCount: 24, Owner: testOwnerView(), StoredDate: "Oct 2025",
	}
}

// TestBinCard_PrivateMarkerOnlyWhenFlagged verifies the lock glyph and its
// sr-only "Private" label render only when BinCardView.Private is true —
// the web adapter only ever sets that true for a bin the requester owns
// (see BinsWebHandlers' own doc), so this component-level check is the
// second half of that contract: given Private=true, the marker must
// actually render.
func TestBinCard_PrivateMarkerOnlyWhenFlagged(t *testing.T) {
	t.Run("public bin", func(t *testing.T) {
		view := testBinCardView()
		out := renderString(t, components.BinCard(view))
		if strings.Contains(out, "Private") {
			t.Errorf("BinCard(Private=false) rendered a private marker: %s", out)
		}
	})
	t.Run("private bin", func(t *testing.T) {
		view := testBinCardView()
		view.Private = true
		out := renderString(t, components.BinCard(view))
		if !strings.Contains(out, `<span class="sr-only">Private</span>`) {
			t.Errorf("BinCard(Private=true) missing the sr-only Private label: %s", out)
		}
	})
}

// TestBinCard_FontMonoOnlyOnCode extends shell_test.go's
// TestLayout_FontMonoOnlyOnBinCode to a real card: Space Mono is reserved
// strictly for the bin code, so a card carrying a name, location, and
// stored date alongside its code must still show font-mono exactly once.
func TestBinCard_FontMonoOnlyOnCode(t *testing.T) {
	out := renderString(t, components.BinCard(testBinCardView()))
	if got := strings.Count(out, "font-mono"); got != 1 {
		t.Errorf("font-mono appears %d times in a bin card, want exactly 1 (BinCode only): %s", got, out)
	}
	if !strings.Contains(out, "BIN-A01") {
		t.Error("BinCard did not render the bin's code")
	}
}

// TestBinCard_OwnerAvatarAccessibleName verifies the card's owner avatar
// carries an accessible name identifying the owner, not just a bare
// initial (see OwnerAvatar's own doc).
func TestBinCard_OwnerAvatarAccessibleName(t *testing.T) {
	out := renderString(t, components.BinCard(testBinCardView()))
	if !strings.Contains(out, `aria-label="Owned by Maya"`) {
		t.Errorf("BinCard missing the owner avatar's accessible name: %s", out)
	}
}

// TestBinGrid_EmptyState verifies an empty bin slice renders a message
// instead of a silently blank grid.
func TestBinGrid_EmptyState(t *testing.T) {
	out := renderString(t, components.BinGrid(nil, false))
	if !strings.Contains(out, "No bins yet.") {
		t.Errorf("BinGrid(empty) missing the empty-state message: %s", out)
	}
}

// TestBinGrid_FontMonoConfinedToBinCodes proves the "Space Mono only on bin
// codes" rule holds across a whole grid, not just one card: with three
// cards, font-mono must appear exactly three times.
func TestBinGrid_FontMonoConfinedToBinCodes(t *testing.T) {
	bins := []components.BinCardView{
		{Code: "BIN-A01", Name: "Winter Clothes", Owner: testOwnerView()},
		{Code: "BIN-A02", Name: "Camping Gear", Owner: testOwnerView()},
		{Code: "BIN-A03", Name: "Craft Supplies", Owner: testOwnerView()},
	}
	out := renderString(t, components.BinGrid(bins, false))
	if got := strings.Count(out, "font-mono"); got != len(bins) {
		t.Errorf("font-mono appears %d times for %d cards, want exactly %d", got, len(bins), len(bins))
	}
}

// TestBinDetail_PrivateMarkerOnlyWhenFlagged mirrors
// TestBinCard_PrivateMarkerOnlyWhenFlagged for the detail page's own
// header marker.
func TestBinDetail_PrivateMarkerOnlyWhenFlagged(t *testing.T) {
	detail := components.BinDetailView{ID: "1", Code: "BIN-A01", Name: "Winter Clothes", Owner: testOwnerView()}
	move := components.MoveBinView{BinID: "1", BinCode: "BIN-A01"}

	out := renderString(t, components.BinDetail(detail, move))
	if strings.Contains(out, "Private") {
		t.Errorf("BinDetail(Private=false) rendered a private marker: %s", out)
	}

	detail.Private = true
	out = renderString(t, components.BinDetail(detail, move))
	if !strings.Contains(out, `<span class="sr-only">Private</span>`) {
		t.Errorf("BinDetail(Private=true) missing the sr-only Private label: %s", out)
	}
}

// TestBinDetail_EmptyContentsState verifies a bin with no items renders a
// message rather than an empty list.
func TestBinDetail_EmptyContentsState(t *testing.T) {
	detail := components.BinDetailView{ID: "1", Code: "BIN-A01", Name: "Winter Clothes", Owner: testOwnerView()}
	move := components.MoveBinView{BinID: "1", BinCode: "BIN-A01"}
	out := renderString(t, components.BinDetail(detail, move))
	if !strings.Contains(out, "This bin is empty.") {
		t.Errorf("BinDetail with no items missing the empty-state message: %s", out)
	}
}

// TestLocationIndex_EmptyState verifies a location index with no locations
// renders a message instead of an empty grid.
func TestLocationIndex_EmptyState(t *testing.T) {
	out := renderString(t, components.LocationIndex(components.LocationsView{}))
	if !strings.Contains(out, "No locations yet.") {
		t.Errorf("LocationIndex(empty) missing the empty-state message: %s", out)
	}
}

// TestBinForm_CSRFFieldPresent verifies every bin form (create and edit)
// carries the hidden CSRF field, following users.templ's own pattern.
func TestBinForm_CSRFFieldPresent(t *testing.T) {
	for _, isEdit := range []bool{false, true} {
		view := components.BinFormView{CSRFToken: "test-token", Code: "BIN-A01", Visibility: "public", IsEdit: isEdit}
		out := renderString(t, components.BinForm(view))
		if !strings.Contains(out, `name="csrf_token"`) || !strings.Contains(out, `value="test-token"`) {
			t.Errorf("BinForm(IsEdit=%v) missing the hidden CSRF field: %s", isEdit, out)
		}
	}
}

// TestBinForm_EditHidesCodeAndLocationInputs verifies the edit form never
// renders an editable code or location field — both are immutable after
// creation (see BinFormView's own doc).
func TestBinForm_EditHidesCodeAndLocationInputs(t *testing.T) {
	view := components.BinFormView{CSRFToken: "t", Code: "BIN-A01", Visibility: "public", IsEdit: true}
	out := renderString(t, components.BinForm(view))
	if strings.Contains(out, `name="code"`) {
		t.Errorf("BinForm(IsEdit=true) rendered an editable code field: %s", out)
	}
	if strings.Contains(out, `name="location_id"`) {
		t.Errorf("BinForm(IsEdit=true) rendered an editable location field: %s", out)
	}
	if !strings.Contains(out, "BIN-A01") {
		t.Error("BinForm(IsEdit=true) missing the read-only code display")
	}
}

// TestMoveBinForm_RendersLocationOptions verifies every candidate location
// renders as a picker option, with the current one pre-selected.
func TestMoveBinForm_RendersLocationOptions(t *testing.T) {
	view := components.MoveBinView{
		BinID: "1", BinCode: "BIN-A01", CurrentLocationID: "loc-1",
		Locations: []components.LocationOptionView{{ID: "loc-1", Name: "Garage"}, {ID: "loc-2", Name: "Attic"}},
	}
	out := renderString(t, components.MoveBinForm(view))
	if !strings.Contains(out, `value="loc-1" selected`) {
		t.Errorf("MoveBinForm did not pre-select the bin's current location: %s", out)
	}
	if !strings.Contains(out, "Attic") {
		t.Error("MoveBinForm missing a candidate location option")
	}
}

// TestBinsPage_NoExternalHost extends shell_test.go's
// TestLayout_NoExternalHost to the full /bins page, including the new bin
// grid and Alpine view-switch wiring.
func TestBinsPage_NoExternalHost(t *testing.T) {
	page := components.BinsPage(components.BinsPageView{
		Toolbar: components.ToolbarView{Heading: "All bins", Count: "1 container"},
		Bins:    []components.BinCardView{testBinCardView()},
	})
	out := renderString(t, components.Layout(testShellProps(), testNav(), page))

	if externalHostPattern.MatchString(out) {
		t.Error("rendered bins page contains an absolute or scheme-relative src/href")
	}
	for _, host := range deniedHosts {
		if strings.Contains(out, host) {
			t.Errorf("rendered bins page references denied host %q", host)
		}
	}
}
