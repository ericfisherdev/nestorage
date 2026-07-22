package components_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/ericfisherdev/nestorage/web/components"
)

func renderString(t *testing.T, c templ.Component) string {
	t.Helper()
	var sb strings.Builder
	if err := c.Render(context.Background(), &sb); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return sb.String()
}

func testNav() []components.NavItem {
	return []components.NavItem{
		{Label: "All bins", Href: "/bins", Active: true, Icon: components.IconBin("h-5 w-5")},
		{Label: "Search & find", Href: "/search", Icon: components.IconSearch("h-5 w-5")},
		{Label: "Categories", Href: "/categories", Icon: components.IconCategories("h-5 w-5")},
	}
}

func testOwners() []components.OwnerView {
	return []components.OwnerView{
		{Name: "Maya", Initials: "M", Color: components.OwnerIndigo},
		{Name: "Daniel", Initials: "D", Color: components.OwnerSteel},
		{Name: "Ivy", Initials: "I", Color: components.OwnerTeal},
		{Name: "Leo", Initials: "L", Color: components.OwnerPeri},
		{Name: "Family", Initials: "F", Color: components.OwnerShared},
	}
}

func testShellProps() components.ShellProps {
	return components.ShellProps{
		Title:  "All bins",
		Owners: testOwners(),
		Stats:  components.ShellStats{Bins: 8, Items: 201, Rooms: 5},
	}
}

// externalHostPattern catches an absolute or scheme-relative URL in an src
// or href attribute — "https://…" and the protocol-relative "//…" form
// browsers also fetch. A relative path ("/static/…") never matches.
var externalHostPattern = regexp.MustCompile(`(?:src|href)\s*=\s*"(?:https?:)?//`)

// deniedHosts are substrings that would identify a CDN or a Google Fonts
// request even if externalHostPattern's attribute-shaped regex somehow
// missed the containing markup.
var deniedHosts = []string{"fonts.googleapis.com", "fonts.gstatic.com", "cdn.", "unpkg", "jsdelivr"}

// TestLayout_NoExternalHost is the acceptance criterion's teeth: the
// appliance has to render with the internet down, so nothing in the shell
// may request a host off the local server. This must catch a stray CDN
// link before it ever ships, not after.
func TestLayout_NoExternalHost(t *testing.T) {
	content := components.BinCode("BIN-A01")
	page := renderString(t, components.Layout(testShellProps(), testNav(), content))

	if externalHostPattern.MatchString(page) {
		t.Error("rendered shell contains an absolute or scheme-relative src/href — the appliance must render with the internet down")
	}
	for _, host := range deniedHosts {
		if strings.Contains(page, host) {
			t.Errorf("rendered shell references denied host %q", host)
		}
	}
}

// TestOwnerAvatar_ColorTriples verifies every OwnerColor renders its tint
// background and its matching foreground from the same palette key, so a
// tint from one owner's set can never be paired with a foreground from
// another.
func TestOwnerAvatar_ColorTriples(t *testing.T) {
	tests := []struct {
		color components.OwnerColor
	}{
		{components.OwnerIndigo},
		{components.OwnerSteel},
		{components.OwnerTeal},
		{components.OwnerPeri},
		{components.OwnerShared},
	}
	for _, tt := range tests {
		t.Run(tt.color.String(), func(t *testing.T) {
			o := components.OwnerView{Name: "Test", Initials: "T", Color: tt.color}
			out := renderString(t, components.OwnerAvatar(o))

			wantTint := "bg-owner-" + tt.color.String() + "-tint"
			wantFg := "text-owner-" + tt.color.String() + "-fg"
			if !strings.Contains(out, wantTint) {
				t.Errorf("OwnerAvatar(%s) missing tint class %q: %s", tt.color, wantTint, out)
			}
			if !strings.Contains(out, wantFg) {
				t.Errorf("OwnerAvatar(%s) missing fg class %q: %s", tt.color, wantFg, out)
			}
		})
	}
}

// TestParseOwnerColor_UnknownFallsBackToShared guards the safelist
// guarantee: ParseOwnerColor can never hand back a value outside the five
// keys @source inline generates classes for.
func TestParseOwnerColor_UnknownFallsBackToShared(t *testing.T) {
	if got := components.ParseOwnerColor("not-a-real-color"); got != components.OwnerShared {
		t.Errorf("ParseOwnerColor(unknown) = %q, want %q", got, components.OwnerShared)
	}
}

// TestLayout_FontMonoOnlyOnBinCode verifies font-mono — reserved strictly
// for bin codes — appears exactly once in a shell that renders one BinCode
// alongside the full sidebar and nav chrome, and nowhere else.
func TestLayout_FontMonoOnlyOnBinCode(t *testing.T) {
	content := components.BinCode("BIN-A01")
	page := renderString(t, components.Layout(testShellProps(), testNav(), content))

	if got := strings.Count(page, "font-mono"); got != 1 {
		t.Errorf("font-mono appears %d times in the rendered shell, want exactly 1 (BinCode only): %s", got, page)
	}
}

// TestSidebar_ExactlyOneActiveNavEntry verifies aria-current="page" marks
// exactly one nav entry — the one NavItem.Active is true for.
func TestSidebar_ExactlyOneActiveNavEntry(t *testing.T) {
	content := components.BinCode("BIN-A01")
	page := renderString(t, components.Layout(testShellProps(), testNav(), content))

	if got := strings.Count(page, `aria-current="page"`); got != 1 {
		t.Errorf(`aria-current="page" appears %d times, want exactly 1: %s`, got, page)
	}
}
