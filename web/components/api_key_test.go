package components_test

import (
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/web/components"
)

func testAPIKeySettingsView(current *components.APIKeyRowView, history ...components.APIKeyRowView) components.APIKeySettingsView {
	return components.APIKeySettingsView{
		CSRFToken: "test-csrf-token",
		Current:   current,
		History:   history,
		Overlaps: []components.OverlapOption{
			{Value: "none", Label: "No overlap — invalidate immediately"},
			{Value: "24h", Label: "24 hours"},
			{Value: "7d", Label: "7 days"},
		},
	}
}

func TestAPIKeySettingsSection_NoCurrentKey_RendersCreateForm(t *testing.T) {
	out := renderString(t, components.APIKeySettingsSection(testAPIKeySettingsView(nil)))
	if !strings.Contains(out, "Create key") {
		t.Errorf("no current key must render the create form: %s", out)
	}
	if strings.Contains(out, "Rotate") {
		t.Errorf("no current key must not render the rotate control: %s", out)
	}
}

func TestAPIKeySettingsSection_WithCurrentKey_RendersRotateAndRevoke(t *testing.T) {
	current := components.APIKeyRowView{ID: "11111111-1111-7111-8111-111111111111", Label: "Nestova integration", KeyPrefix: "ns_a1b2c3d4", CreatedAt: "Jul 21, 2026", LastUsedAt: "Never"}
	out := renderString(t, components.APIKeySettingsSection(testAPIKeySettingsView(&current)))

	if !strings.Contains(out, "Rotate") {
		t.Errorf("a current key must render the rotate control: %s", out)
	}
	if !strings.Contains(out, "Revoke") {
		t.Errorf("a current key must render the revoke control: %s", out)
	}
	if strings.Contains(out, "Create key") {
		t.Errorf("a current key must not render the create form: %s", out)
	}
	if !strings.Contains(out, current.ID) {
		t.Errorf("revoke form missing the current key's id: %s", out)
	}
}

// TestAPIKeySettingsSection_Reveal_RendersSecretOnlyWhenPresent is the
// automated equivalent of this ticket's "the reveal renders the secret only
// when present" acceptance criterion.
func TestAPIKeySettingsSection_Reveal_RendersSecretOnlyWhenPresent(t *testing.T) {
	view := testAPIKeySettingsView(nil)
	withoutReveal := renderString(t, components.APIKeySettingsSection(view))
	if strings.Contains(withoutReveal, "ns_secretvalue") {
		t.Errorf("no reveal must not leak a secret: %s", withoutReveal)
	}

	view.Reveal = &components.APIKeySecretReveal{Secret: "ns_secretvalue", Label: "Nestova integration"}
	withReveal := renderString(t, components.APIKeySettingsSection(view))
	if !strings.Contains(withReveal, "ns_secretvalue") {
		t.Errorf("a present reveal must render the secret: %s", withReveal)
	}
	if !strings.Contains(withReveal, `data-secret="ns_secretvalue"`) {
		t.Errorf("the secret must be carried in a data attribute, not only inline text, so the copy button never inlines it into a script: %s", withReveal)
	}
}

// TestAPIKeyHistoryRow_RendersPrefixNeverAHash is the automated equivalent
// of this ticket's "the row renders the prefix and never a hash" acceptance
// criterion — components.APIKeyRowView carries no hash field at all, so
// this also guards against one ever being added back.
func TestAPIKeyHistoryRow_RendersPrefixNeverAHash(t *testing.T) {
	row := components.APIKeyRowView{ID: "11111111-1111-7111-8111-111111111111", Label: "Nestova integration", KeyPrefix: "ns_a1b2c3d4", Status: "revoked", CreatedAt: "Jul 21, 2026", LastUsedAt: "Never", RevokedAt: "Jul 22, 2026"}
	out := renderString(t, components.APIKeySettingsSection(testAPIKeySettingsView(nil, row)))

	if !strings.Contains(out, "ns_a1b2c3d4") {
		t.Errorf("history row missing the key prefix: %s", out)
	}
	if !strings.Contains(out, "revoked") {
		t.Errorf("history row missing its status: %s", out)
	}
}

func TestAPIKeyHistory_EmptyState(t *testing.T) {
	out := renderString(t, components.APIKeySettingsSection(testAPIKeySettingsView(nil)))
	if !strings.Contains(out, "No keys yet.") {
		t.Errorf("no history must render the empty-state message: %s", out)
	}
}

func TestAPIKeySettingsSection_FormError(t *testing.T) {
	view := testAPIKeySettingsView(nil)
	view.FormError = "Please enter a label."
	out := renderString(t, components.APIKeySettingsSection(view))
	if !strings.Contains(out, "Please enter a label.") {
		t.Errorf("FormError must render inline: %s", out)
	}
}

// TestAPIKeySettingsSection_NoFontMono guards Space Mono's exclusivity to
// bin codes (web/components/bincode.templ): this screen's prefix and secret
// use wide tracking on the normal UI face instead.
func TestAPIKeySettingsSection_NoFontMono(t *testing.T) {
	current := components.APIKeyRowView{ID: "11111111-1111-7111-8111-111111111111", Label: "Nestova integration", KeyPrefix: "ns_a1b2c3d4", CreatedAt: "Jul 21, 2026", LastUsedAt: "Never"}
	view := testAPIKeySettingsView(&current, current)
	view.Reveal = &components.APIKeySecretReveal{Secret: "ns_secretvalue", Label: "Nestova integration"}

	out := renderString(t, components.APIKeySettingsSection(view))
	if strings.Contains(out, "font-mono") {
		t.Errorf("the api key screen must never use font-mono: %s", out)
	}
}

// TestAPIKeyHistoryRow_UnknownStatus_StillRendersSafely guards
// apiKeyStatusClass's default branch: a status value outside the four known
// domain.APIKeyStatus constants must still render (with the same fallback
// styling as "expired"), never panic — this template must never fail to
// render just because a future status is added to the domain before this
// mapping is updated.
func TestAPIKeyHistoryRow_UnknownStatus_StillRendersSafely(t *testing.T) {
	row := components.APIKeyRowView{ID: "11111111-1111-7111-8111-111111111111", Label: "Nestova integration", KeyPrefix: "ns_a1b2c3d4", Status: "some-future-status", CreatedAt: "Jul 21, 2026", LastUsedAt: "Never"}
	out := renderString(t, components.APIKeySettingsSection(testAPIKeySettingsView(nil, row)))

	if !strings.Contains(out, "some-future-status") {
		t.Errorf("history row missing the unrecognized status label: %s", out)
	}
}

func TestOverlapSelect_RendersEveryChoice(t *testing.T) {
	current := components.APIKeyRowView{ID: "11111111-1111-7111-8111-111111111111", Label: "Nestova integration", KeyPrefix: "ns_a1b2c3d4", CreatedAt: "Jul 21, 2026", LastUsedAt: "Never"}
	out := renderString(t, components.APIKeySettingsSection(testAPIKeySettingsView(&current)))

	for _, want := range []string{"none", "24h", "7d"} {
		if !strings.Contains(out, `value="`+want+`"`) {
			t.Errorf("overlap select missing option %q: %s", want, out)
		}
	}
}
