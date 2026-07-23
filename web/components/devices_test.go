package components_test

import (
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/web/components"
)

func testDevicesView(rows ...components.DeviceView) components.DevicesView {
	return components.DevicesView{CSRFToken: "test-csrf-token", Devices: rows}
}

func TestDeviceList_EmptyState(t *testing.T) {
	out := renderString(t, components.DeviceList(testDevicesView()))
	if !strings.Contains(out, "No devices yet.") {
		t.Errorf("DeviceList() with no devices missing the empty-state message: %s", out)
	}
}

func TestDeviceList_RendersNameAndDates(t *testing.T) {
	view := testDevicesView(components.DeviceView{
		ID: "11111111-1111-7111-8111-111111111111", Name: "Maya Phone",
		CreatedAt: "Jul 21, 2026", LastUsedAt: "Never",
	})
	out := renderString(t, components.DeviceList(view))

	if !strings.Contains(out, "Maya Phone") {
		t.Errorf("DeviceList() missing device name: %s", out)
	}
	if !strings.Contains(out, "Jul 21, 2026") {
		t.Errorf("DeviceList() missing CreatedAt: %s", out)
	}
	if !strings.Contains(out, "Never") {
		t.Errorf("DeviceList() missing the Never last-used fallback: %s", out)
	}
}

func TestDeviceList_CSRFTokenInEveryRowForm(t *testing.T) {
	view := testDevicesView(
		components.DeviceView{ID: "11111111-1111-7111-8111-111111111111", Name: "Phone", CreatedAt: "Jul 21, 2026", LastUsedAt: "Never"},
		components.DeviceView{ID: "22222222-2222-7222-8222-222222222222", Name: "Tablet", CreatedAt: "Jul 20, 2026", LastUsedAt: "Jul 22, 2026"},
	)
	out := renderString(t, components.DeviceList(view))

	if got := strings.Count(out, `name="csrf_token" value="test-csrf-token"`); got != 2 {
		t.Errorf("csrf_token hidden field appears %d times, want 2 (one per device row): %s", got, out)
	}
}

func TestDeviceList_RevokeFormTargetsDeviceRoute(t *testing.T) {
	view := testDevicesView(components.DeviceView{
		ID: "11111111-1111-7111-8111-111111111111", Name: "Phone", CreatedAt: "Jul 21, 2026", LastUsedAt: "Never",
	})
	out := renderString(t, components.DeviceList(view))

	const wantAction = "/settings/devices/11111111-1111-7111-8111-111111111111/revoke"
	if !strings.Contains(out, wantAction) {
		t.Errorf("DeviceList() row action = missing %q: %s", wantAction, out)
	}
	if !strings.Contains(out, "Revoke") {
		t.Errorf("DeviceList() missing the Revoke button label: %s", out)
	}
}
