package components_test

import (
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/web/components"
)

func TestItemLinksSection_RendersLinkAndAttributes(t *testing.T) {
	view := components.ItemLinksView{
		ItemID:    "item-1",
		CSRFToken: "tok",
		Links: []components.ItemLinkView{
			{ID: "link-1", Label: "Owner's manual", URL: "https://example.com/manual.pdf"},
		},
	}
	out := renderString(t, components.ItemLinksSection(view))

	if !strings.Contains(out, `id="item-links"`) {
		t.Error("ItemLinksSection() missing its own id, which the add/edit/delete forms' hx-target relies on")
	}
	if !strings.Contains(out, "Owner&#39;s manual") && !strings.Contains(out, "Owner's manual") {
		t.Errorf("ItemLinksSection() missing the link label: %s", out)
	}
	if !strings.Contains(out, `href="https://example.com/manual.pdf"`) {
		t.Errorf("ItemLinksSection() missing the sanitized href: %s", out)
	}
	if !strings.Contains(out, `target="_blank"`) || !strings.Contains(out, `rel="noopener noreferrer"`) {
		t.Errorf("ItemLinksSection() link missing target=_blank/rel=noopener noreferrer (tabnabbing guard): %s", out)
	}
}

func TestItemLinksSection_EmptyState(t *testing.T) {
	view := components.ItemLinksView{ItemID: "item-1", CSRFToken: "tok"}
	out := renderString(t, components.ItemLinksSection(view))

	if !strings.Contains(out, "No links yet.") {
		t.Errorf("ItemLinksSection() missing the empty state: %s", out)
	}
}

func TestItemLinksSection_FormErrorRendered(t *testing.T) {
	view := components.ItemLinksView{ItemID: "item-1", CSRFToken: "tok", FormError: "Only http and https links are allowed."}
	out := renderString(t, components.ItemLinksSection(view))

	if !strings.Contains(out, "Only http and https links are allowed.") {
		t.Errorf("ItemLinksSection() missing FormError: %s", out)
	}
	if !strings.Contains(out, `role="alert"`) {
		t.Errorf("ItemLinksSection() FormError missing role=alert: %s", out)
	}
}

// TestItemLinksSection_JavascriptURLSanitizedAtRenderTime proves
// itemLinkAnchor's own defense-in-depth: even if an unsafe-scheme URL
// somehow reached this template (domain.ValidateItemLinkURL should already
// have rejected it), templ.URL neutralizes it rather than emitting a
// javascript: href — the AC's "XSS attempt is rendered inert" covered at
// the render layer, complementing the adapter-level end-to-end test.
func TestItemLinksSection_JavascriptURLSanitizedAtRenderTime(t *testing.T) {
	view := components.ItemLinksView{
		ItemID: "item-1", CSRFToken: "tok",
		Links: []components.ItemLinkView{{ID: "link-1", Label: "<script>alert(1)</script>", URL: "javascript:alert(1)"}},
	}
	out := renderString(t, components.ItemLinksSection(view))

	if strings.Contains(out, `href="javascript:`) {
		t.Errorf("ItemLinksSection() rendered a raw javascript: href: %s", out)
	}
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Errorf("ItemLinksSection() rendered an unescaped label: %s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("ItemLinksSection() label was not escaped: %s", out)
	}
}

func TestItemLinksSection_EditFormPrefilled(t *testing.T) {
	view := components.ItemLinksView{
		ItemID: "item-1", CSRFToken: "tok",
		Links: []components.ItemLinkView{{ID: "link-1", Label: "Manual", URL: "https://example.com/manual"}},
	}
	out := renderString(t, components.ItemLinksSection(view))

	if !strings.Contains(out, `value="Manual"`) {
		t.Errorf("ItemLinksSection() edit form missing prefilled label: %s", out)
	}
	if !strings.Contains(out, `value="https://example.com/manual"`) {
		t.Errorf("ItemLinksSection() edit form missing prefilled url: %s", out)
	}
	if !strings.Contains(out, `action="/items/item-1/links/link-1"`) {
		t.Errorf("ItemLinksSection() edit form missing its own action: %s", out)
	}
	if !strings.Contains(out, `action="/items/item-1/links/link-1/delete"`) {
		t.Errorf("ItemLinksSection() delete form missing its own action: %s", out)
	}
	if !strings.Contains(out, `action="/items/item-1/links"`) {
		t.Errorf("ItemLinksSection() add form missing its own action: %s", out)
	}
}
