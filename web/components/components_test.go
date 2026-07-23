package components_test

import (
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/ericfisherdev/nestorage/web/components"
)

// TestIcons_RenderDecorative table-drives every icon component: each must
// render non-empty markup that is aria-hidden and unfocusable, since every
// icon in this package is decorative — the accessible label always comes
// from surrounding text or an sr-only span at the call site.
func TestIcons_RenderDecorative(t *testing.T) {
	tests := []struct {
		name string
		icon templ.Component
	}{
		{"IconBin", components.IconBin("h-5 w-5")},
		{"IconBox", components.IconBox("h-5 w-5")},
		{"IconSearch", components.IconSearch("h-5 w-5")},
		{"IconCategories", components.IconCategories("h-5 w-5")},
		{"IconLocations", components.IconLocations("h-5 w-5")},
		{"IconPin", components.IconPin("h-5 w-5")},
		{"IconLabels", components.IconLabels("h-5 w-5")},
		{"IconPlus", components.IconPlus("h-5 w-5")},
		{"IconGrid", components.IconGrid("h-5 w-5")},
		{"IconList", components.IconList("h-5 w-5")},
		{"IconCheck", components.IconCheck("h-5 w-5")},
		{"IconUsers", components.IconUsers("h-5 w-5")},
		{"IconCalendar", components.IconCalendar("h-5 w-5")},
		{"IconCount", components.IconCount("h-5 w-5")},
		{"IconLock", components.IconLock("h-5 w-5")},
		{"IconMove", components.IconMove("h-5 w-5")},
		{"IconPencil", components.IconPencil("h-5 w-5")},
		{"IconTrash", components.IconTrash("h-5 w-5")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := renderString(t, tt.icon)
			if !strings.Contains(out, "<svg") {
				t.Errorf("%s did not render an <svg>: %s", tt.name, out)
			}
			if !strings.Contains(out, `aria-hidden="true"`) {
				t.Errorf("%s missing aria-hidden=\"true\": %s", tt.name, out)
			}
			if !strings.Contains(out, `focusable="false"`) {
				t.Errorf("%s missing focusable=\"false\": %s", tt.name, out)
			}
		})
	}
}

func TestTag(t *testing.T) {
	out := renderString(t, components.Tag("Seasonal"))
	if !strings.Contains(out, "Seasonal") {
		t.Errorf("Tag() missing label: %s", out)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "<li") {
		t.Errorf("Tag() should render a bare <li> for a caller's <ul>: %s", out)
	}
}

func TestSearchInput(t *testing.T) {
	out := renderString(t, components.SearchInput("bin-search", "Search bins and items", "Search bins & items"))
	if !strings.Contains(out, `id="bin-search"`) {
		t.Errorf("SearchInput() missing input id: %s", out)
	}
	if !strings.Contains(out, `for="bin-search"`) {
		t.Errorf("SearchInput() label's for does not match the input id: %s", out)
	}
	if !strings.Contains(out, `type="search"`) {
		t.Errorf("SearchInput() should render type=\"search\": %s", out)
	}
}

func TestViewSwitch_PressedStateMatchesActive(t *testing.T) {
	grid := renderString(t, components.ViewSwitch(components.ViewGrid))
	if !strings.Contains(grid, `Grid view`) || !strings.Contains(grid, `List view`) {
		t.Errorf("ViewSwitch() missing an sr-only label: %s", grid)
	}
	if strings.Count(grid, `aria-pressed="true"`) != 1 {
		t.Errorf("ViewSwitch(ViewGrid) should mark exactly one button pressed: %s", grid)
	}

	list := renderString(t, components.ViewSwitch(components.ViewList))
	if strings.Count(list, `aria-pressed="true"`) != 1 {
		t.Errorf("ViewSwitch(ViewList) should mark exactly one button pressed: %s", list)
	}
}

func TestFilterPill_ActiveState(t *testing.T) {
	attrs := templ.Attributes{"hx-get": "/bins"}

	active := renderString(t, components.FilterPill("All", true, attrs))
	if !strings.Contains(active, `aria-pressed="true"`) {
		t.Errorf("FilterPill(active=true) missing aria-pressed=true: %s", active)
	}
	if !strings.Contains(active, `hx-get="/bins"`) {
		t.Errorf("FilterPill() did not spread its attrs: %s", active)
	}

	inactive := renderString(t, components.FilterPill("Seasonal", false, attrs))
	if !strings.Contains(inactive, `aria-pressed="false"`) {
		t.Errorf("FilterPill(active=false) missing aria-pressed=false: %s", inactive)
	}
}

func TestToolbar_RendersCategoriesAndMarksActive(t *testing.T) {
	view := components.ToolbarView{
		Heading:    "All bins",
		Count:      "8 containers",
		Categories: []string{"All", "Seasonal", "Tools"},
		Active:     "Seasonal",
	}
	out := renderString(t, components.Toolbar(view))

	if !strings.Contains(out, "All bins") || !strings.Contains(out, "8 containers") {
		t.Errorf("Toolbar() missing heading or count: %s", out)
	}
	for _, category := range view.Categories {
		want := `aria-pressed="false"`
		if category == view.Active {
			want = `aria-pressed="true"`
		}
		if !strings.Contains(out, `>`+category+`</button>`) {
			t.Errorf("Toolbar() missing category %q: %s", category, out)
			continue
		}
		pill := out[:strings.Index(out, `>`+category+`</button>`)]
		pill = pill[strings.LastIndex(pill, "<button"):]
		if !strings.Contains(pill, want) {
			t.Errorf("Toolbar() category %q pill missing %s: %s", category, want, pill)
		}
	}
	if !strings.Contains(out, `id="bin-toolbar"`) {
		t.Error("Toolbar() missing its own id, which the filter pills' hx-target relies on")
	}
	if !strings.Contains(out, `hx-target="#bin-toolbar"`) || !strings.Contains(out, `hx-swap="outerHTML"`) {
		t.Errorf("Toolbar() filter pills missing the outerHTML swap wiring: %s", out)
	}
}

func TestBinCode(t *testing.T) {
	out := renderString(t, components.BinCode("BIN-A01"))
	if !strings.Contains(out, "BIN-A01") {
		t.Errorf("BinCode() missing the code: %s", out)
	}
	if !strings.Contains(out, "font-mono") {
		t.Errorf("BinCode() missing font-mono: %s", out)
	}
}
