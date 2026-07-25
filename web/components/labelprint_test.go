package components_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/web/components"
)

// checkedAttrPattern matches the rendered boolean "checked" HTML attribute
// on a radio input — not the "has-[:checked]" Tailwind variant every size
// card's own class list carries regardless of selection, which a naive
// substring count would also match.
var checkedAttrPattern = regexp.MustCompile(`\schecked(\s|>)`)

func testLabelSizeOptions(selectedID string) []components.LabelSizeOptionView {
	return []components.LabelSizeOptionView{
		{ID: "avery-5160", DisplayName: "Avery 5160", CellWidthMM: 66.7, CellHeightMM: 25.4, PerSheetCount: 30, Selected: selectedID == "avery-5160"},
		{ID: "avery-5163", DisplayName: "Avery 5163", CellWidthMM: 101.6, CellHeightMM: 50.8, PerSheetCount: 6, Selected: selectedID == "avery-5163"},
	}
}

func testLabelPreviewView() components.LabelSheetPreviewView {
	return components.LabelSheetPreviewView{
		BinQuery: "", LocationQuery: "loc-1", SizeID: "avery-5160", Offset: 1,
		PageWidthMM: 216, PageHeightMM: 279,
		Cells: []components.LabelPreviewCellView{
			{Index: 0, LeftPercent: 0, TopPercent: 0, WidthPercent: 30, HeightPercent: 9, Dimmed: true},
			{Index: 1, LeftPercent: 30, TopPercent: 0, WidthPercent: 30, HeightPercent: 9, Filled: true, BinCode: "BIN-A01", BinName: "Winter Clothes", LocationName: "Garage"},
			{Index: 2, LeftPercent: 60, TopPercent: 0, WidthPercent: 30, HeightPercent: 9},
		},
		Summary: "1 label across 1 sheet",
	}
}

func testLabelPrintPageView() components.LabelPrintPageView {
	return components.LabelPrintPageView{
		CSRFToken: "test-token", HeaderTitle: "Print labels — Garage", LocationName: "Garage", BinCount: 3,
		Sizes: testLabelSizeOptions("avery-5160"), Preview: testLabelPreviewView(),
	}
}

// TestLabelPrintPage_RendersHeaderAndBinCount verifies the print screen
// names its own target and grounds it in the location it actually belongs
// to, per printTarget's own doc (a bin-scoped request still prints its
// whole containing location).
func TestLabelPrintPage_RendersHeaderAndBinCount(t *testing.T) {
	out := renderString(t, components.LabelPrintPage(testLabelPrintPageView()))
	if !strings.Contains(out, "Print labels — Garage") {
		t.Errorf("LabelPrintPage missing header title: %s", out)
	}
	if !strings.Contains(out, "3 bins") {
		t.Errorf("LabelPrintPage missing bin count: %s", out)
	}
}

// TestLabelPrintPage_CSRFFieldPresent verifies the one mutating route this
// screen posts to (POST /labels/print) carries its CSRF token, following
// TestBinForm_CSRFFieldPresent's own pattern.
func TestLabelPrintPage_CSRFFieldPresent(t *testing.T) {
	out := renderString(t, components.LabelPrintPage(testLabelPrintPageView()))
	if !strings.Contains(out, `name="csrf_token"`) || !strings.Contains(out, `value="test-token"`) {
		t.Errorf("LabelPrintPage missing the hidden CSRF field: %s", out)
	}
}

// TestLabelPrintPage_FormErrorOnlyWhenPresent verifies Print's own mapped
// error (an invalid size, an out-of-range offset, or NSTR-50's batch cap)
// renders inline only when set — a successful GET must not show a bare
// alert box.
func TestLabelPrintPage_FormErrorOnlyWhenPresent(t *testing.T) {
	view := testLabelPrintPageView()
	out := renderString(t, components.LabelPrintPage(view))
	if strings.Contains(out, `role="alert"`) {
		t.Errorf("LabelPrintPage without a FormError rendered an alert: %s", out)
	}

	view.FormError = "Please choose a valid label size."
	out = renderString(t, components.LabelPrintPage(view))
	if !strings.Contains(out, `role="alert"`) || !strings.Contains(out, view.FormError) {
		t.Errorf("LabelPrintPage with FormError set missing the inline alert: %s", out)
	}
}

// TestLabelSizePicker_SelectedSizeIsChecked verifies exactly one size card
// is preselected, and that it is the caller-selected one — the remembered
// size, or the registry default, per preferredSizeID's own doc.
func TestLabelSizePicker_SelectedSizeIsChecked(t *testing.T) {
	sizes := testLabelSizeOptions("avery-5163")
	out := renderString(t, components.LabelPrintPage(components.LabelPrintPageView{
		CSRFToken: "t", Sizes: sizes, Preview: testLabelPreviewView(),
	}))

	if got := checkedAttrPattern.FindAllString(out, -1); len(got) != 1 {
		t.Errorf("size picker should mark exactly one radio checked, found %v: %s", got, out)
	}
	idx := strings.Index(out, `value="avery-5163"`)
	if idx < 0 || !checkedAttrPattern.MatchString(out[idx:idx+120]) {
		t.Errorf("avery-5163's own radio is not the one marked checked: %s", out)
	}
}

// TestLabelSheetPreview_FilledCellShowsCodeNameAndLocation verifies an
// occupied cell renders the bin's own code, name, and containing location —
// per NSTR-51's plan ("occupied cells show the bin name (truncated),
// location as secondary text, ... the bin code uses the existing BinCode
// component").
func TestLabelSheetPreview_FilledCellShowsCodeNameAndLocation(t *testing.T) {
	out := renderString(t, components.LabelSheetPreview(testLabelPreviewView()))
	if !strings.Contains(out, "BIN-A01") {
		t.Errorf("preview missing the filled cell's own bin code: %s", out)
	}
	if !strings.Contains(out, "Winter Clothes") {
		t.Errorf("preview missing the filled cell's own bin name: %s", out)
	}
	if !strings.Contains(out, "Garage") {
		t.Errorf("preview missing the filled cell's own location name: %s", out)
	}
}

// TestLabelSheetPreview_DimmedCellRendersNoBinDetails verifies a dimmed
// cell (before the chosen start offset) never renders bin details even if
// its own view somehow still carried them — the template gates on Filled,
// not on whether the fields happen to be empty.
func TestLabelSheetPreview_DimmedCellRendersNoBinDetails(t *testing.T) {
	view := components.LabelSheetPreviewView{
		Cells: []components.LabelPreviewCellView{{Index: 0, Dimmed: true, BinCode: "SHOULD-NOT-RENDER"}},
	}
	out := renderString(t, components.LabelSheetPreview(view))
	if strings.Contains(out, "SHOULD-NOT-RENDER") {
		t.Errorf("a dimmed cell must not render bin details: %s", out)
	}
}

// TestLabelSheetPreview_HiddenInputsCarryFormState verifies the preview
// fragment's own hidden bin/location/offset inputs travel with the visual —
// per LabelSheetPreviewView's own doc, so an htmx refresh never leaves the
// picture and the form's pending values disagreeing.
func TestLabelSheetPreview_HiddenInputsCarryFormState(t *testing.T) {
	view := testLabelPreviewView()
	view.BinQuery = "bin-1"
	out := renderString(t, components.LabelSheetPreview(view))
	if !strings.Contains(out, `name="bin" value="bin-1"`) {
		t.Errorf("preview missing its own hidden bin input: %s", out)
	}
	if !strings.Contains(out, `name="location" value="loc-1"`) {
		t.Errorf("preview missing its own hidden location input: %s", out)
	}
	if !strings.Contains(out, `name="offset" value="1"`) {
		t.Errorf("preview missing its own hidden offset input: %s", out)
	}
}

// TestLabelSheetPreview_SummaryLineRenders verifies the pre-rendered
// "N labels across M sheets" line shows up as-is.
func TestLabelSheetPreview_SummaryLineRenders(t *testing.T) {
	out := renderString(t, components.LabelSheetPreview(testLabelPreviewView()))
	if !strings.Contains(out, "1 label across 1 sheet") {
		t.Errorf("preview missing its own summary line: %s", out)
	}
}

// TestLabelPrintPage_FontMonoConfinedToBinCodes extends BinCard's own
// TestBinCard_FontMonoOnlyOnCode to the print screen: font-mono must appear
// exactly once per filled preview cell (BinCode's own component), never on
// the bin name or location text beside it.
func TestLabelPrintPage_FontMonoConfinedToBinCodes(t *testing.T) {
	view := testLabelPrintPageView()
	out := renderString(t, components.LabelPrintPage(view))
	filled := 0
	for _, c := range view.Preview.Cells {
		if c.Filled {
			filled++
		}
	}
	if got := strings.Count(out, "font-mono"); got != filled {
		t.Errorf("font-mono appears %d times for %d filled cells, want exactly %d (BinCode only)", got, filled, filled)
	}
}

// TestLabelPrintPage_TouchTargetsMeetMinimumSize verifies the screen's own
// controls carry the 44px minimum touch target the entryway touchscreen
// needs.
func TestLabelPrintPage_TouchTargetsMeetMinimumSize(t *testing.T) {
	out := renderString(t, components.LabelPrintPage(testLabelPrintPageView()))
	if !strings.Contains(out, "min-h-[44px]") {
		t.Error("LabelPrintPage missing 44px touch targets")
	}
}

// TestLabelPrintPage_NoExternalHost extends shell_test.go's
// TestLayout_NoExternalHost to the full print screen — no new JS, no
// external hosts, per NSTR-51's own plan.
func TestLabelPrintPage_NoExternalHost(t *testing.T) {
	page := components.LabelPrintPage(testLabelPrintPageView())
	out := renderString(t, components.Layout(testShellProps(), testNav(), page))

	if externalHostPattern.MatchString(out) {
		t.Error("rendered label print page contains an absolute or scheme-relative src/href")
	}
	for _, host := range deniedHosts {
		if strings.Contains(out, host) {
			t.Errorf("rendered label print page references denied host %q", host)
		}
	}
}
