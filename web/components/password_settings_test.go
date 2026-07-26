package components_test

import (
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/web/components"
)

func TestPasswordSettings_Standalone_RendersForm(t *testing.T) {
	view := components.PasswordSettingsView{CSRFToken: "test-csrf-token"}
	out := renderString(t, components.PasswordSettings(view))

	if !strings.Contains(out, `name="current_password"`) {
		t.Errorf("PasswordSettings() missing the current-password field: %s", out)
	}
	if !strings.Contains(out, `name="new_password"`) {
		t.Errorf("PasswordSettings() missing the new-password field: %s", out)
	}
	if !strings.Contains(out, `name="new_password_confirmation"`) {
		t.Errorf("PasswordSettings() missing the confirmation field: %s", out)
	}
	if !strings.Contains(out, `value="test-csrf-token"`) {
		t.Errorf("PasswordSettings() missing the CSRF token: %s", out)
	}
}

func TestPasswordSettings_Standalone_ShowsError(t *testing.T) {
	view := components.PasswordSettingsView{CSRFToken: "tok", Error: "Current password is incorrect."}
	out := renderString(t, components.PasswordSettings(view))

	if !strings.Contains(out, "Current password is incorrect.") {
		t.Errorf("PasswordSettings() missing the error message: %s", out)
	}
}

func TestPasswordSettings_Standalone_ShowsSuccess(t *testing.T) {
	view := components.PasswordSettingsView{CSRFToken: "tok", Success: "Your password has been changed."}
	out := renderString(t, components.PasswordSettings(view))

	if !strings.Contains(out, "Your password has been changed.") {
		t.Errorf("PasswordSettings() missing the success message: %s", out)
	}
}

// TestPasswordSettings_Federated_NoFormLinksProvider is the automated
// equivalent of this ticket's "in federated mode the interface does not
// present a change-password form" criterion, at the template layer.
func TestPasswordSettings_Federated_NoFormLinksProvider(t *testing.T) {
	view := components.PasswordSettingsView{Federated: true, ProviderURL: "https://provider.example.com"}
	out := renderString(t, components.PasswordSettings(view))

	if strings.Contains(out, `name="current_password"`) {
		t.Errorf("PasswordSettings(federated) must not render the password form: %s", out)
	}
	if !strings.Contains(out, `href="https://provider.example.com"`) {
		t.Errorf("PasswordSettings(federated) missing the provider link: %s", out)
	}
}
