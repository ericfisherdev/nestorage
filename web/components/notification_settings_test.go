package components_test

import (
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/web/components"
)

func testNotificationSettingsView(sections ...components.EventSection) components.NotificationSettingsView {
	return components.NotificationSettingsView{CSRFToken: "test-csrf-token", Sections: sections}
}

func TestNotificationSettings_RendersEachSectionLabel(t *testing.T) {
	view := testNotificationSettingsView(
		components.EventSection{EventType: "return_requested", Label: "Return requested", EmailEnabled: false},
		components.EventSection{EventType: "item_returned", Label: "Item returned", EmailEnabled: false},
	)
	out := renderString(t, components.NotificationSettings(view))

	if !strings.Contains(out, "Return requested") {
		t.Errorf("NotificationSettings() missing the return_requested section label: %s", out)
	}
	if !strings.Contains(out, "Item returned") {
		t.Errorf("NotificationSettings() missing the item_returned section label: %s", out)
	}
}

func TestNotificationSettings_InAppRowIsAlwaysOnNeverACheckbox(t *testing.T) {
	view := testNotificationSettingsView(
		components.EventSection{EventType: "return_requested", Label: "Return requested", EmailEnabled: false},
	)
	out := renderString(t, components.NotificationSettings(view))

	if !strings.Contains(out, "Always on") {
		t.Errorf("NotificationSettings() missing the always-on in-app row: %s", out)
	}
	// The in-app row itself must render no <input>: the only checkbox
	// anywhere on the page is the email toggle.
	if strings.Count(out, `type="checkbox"`) != 1 {
		t.Errorf("NotificationSettings() rendered %d checkboxes for one section, want exactly 1 (email only, in-app is not a switch): %s", strings.Count(out, `type="checkbox"`), out)
	}
}

func TestNotificationSettings_EmailToggle_ReflectsEnabledState(t *testing.T) {
	enabledView := testNotificationSettingsView(
		components.EventSection{EventType: "return_requested", Label: "Return requested", EmailEnabled: true},
	)
	disabledView := testNotificationSettingsView(
		components.EventSection{EventType: "return_requested", Label: "Return requested", EmailEnabled: false},
	)

	enabledOut := renderString(t, components.NotificationSettings(enabledView))
	disabledOut := renderString(t, components.NotificationSettings(disabledView))

	if !strings.Contains(enabledOut, "checked") {
		t.Errorf("NotificationSettings() with EmailEnabled=true missing the checked attribute: %s", enabledOut)
	}
	if strings.Contains(disabledOut, "checked") {
		t.Errorf("NotificationSettings() with EmailEnabled=false must not render checked: %s", disabledOut)
	}
}

func TestNotificationSettings_ToggleFormTargetsEventRoute(t *testing.T) {
	view := testNotificationSettingsView(
		components.EventSection{EventType: "item_returned", Label: "Item returned", EmailEnabled: false},
	)
	out := renderString(t, components.NotificationSettings(view))

	const wantAction = "/settings/notifications/item_returned/email"
	if !strings.Contains(out, wantAction) {
		t.Errorf("NotificationSettings() row action = missing %q: %s", wantAction, out)
	}
}

func TestNotificationSettings_CSRFTokenInEveryToggleForm(t *testing.T) {
	view := testNotificationSettingsView(
		components.EventSection{EventType: "return_requested", Label: "Return requested", EmailEnabled: false},
		components.EventSection{EventType: "item_returned", Label: "Item returned", EmailEnabled: false},
	)
	out := renderString(t, components.NotificationSettings(view))

	if got := strings.Count(out, `name="csrf_token" value="test-csrf-token"`); got != 2 {
		t.Errorf("csrf_token hidden field appears %d times, want 2 (one per section form): %s", got, out)
	}
}
