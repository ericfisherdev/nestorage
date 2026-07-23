package components_test

import (
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/web/components"
)

func testUsersView(rows ...components.UserRowView) components.UsersView {
	return components.UsersView{CSRFToken: "test-csrf-token", Users: rows}
}

// TestForbiddenPage_NoExternalHost extends TestLayout_NoExternalHost's
// coverage to RequireAdmin's rendered 403 page: it renders outside Layout
// entirely, so the no-external-host guarantee has to be checked again
// independently.
func TestForbiddenPage_NoExternalHost(t *testing.T) {
	page := renderString(t, components.ForbiddenPage())

	if externalHostPattern.MatchString(page) {
		t.Error("rendered forbidden page contains an absolute or scheme-relative src/href — the appliance must render with the internet down")
	}
	for _, host := range deniedHosts {
		if strings.Contains(page, host) {
			t.Errorf("rendered forbidden page references denied host %q", host)
		}
	}
}

func TestForbiddenPage_MessageAndHomeLink(t *testing.T) {
	page := renderString(t, components.ForbiddenPage())

	if !strings.Contains(page, "Access denied") {
		t.Errorf("forbidden page missing its heading: %s", page)
	}
	if !strings.Contains(page, `href="/"`) {
		t.Errorf("forbidden page missing a link back to /: %s", page)
	}
}

func TestUsersTable_EmptyState(t *testing.T) {
	out := renderString(t, components.UsersTable(testUsersView()))
	if !strings.Contains(out, "No users yet.") {
		t.Errorf("UsersTable() with no users missing the empty-state message: %s", out)
	}
}

func TestUsersTable_CSRFTokenInEveryForm(t *testing.T) {
	view := testUsersView(components.UserRowView{
		ID: "11111111-1111-7111-8111-111111111111", DisplayName: "Maya", Email: "maya@example.com",
		Role: "member", Active: true, Owner: components.OwnerView{Name: "Maya", Initials: "M", Color: components.OwnerIndigo},
	})
	out := renderString(t, components.UsersTable(view))

	// The add-user form plus this one row's three forms (role, deactivate,
	// password) each carry their own hidden CSRF field.
	if got := strings.Count(out, `name="csrf_token" value="test-csrf-token"`); got != 4 {
		t.Errorf("csrf_token hidden field appears %d times, want 4 (add-user + 3 row forms): %s", got, out)
	}
}

func TestUsersTable_FormErrorRendersAlert(t *testing.T) {
	view := testUsersView()
	view.FormError = "Cannot remove the last active admin."
	out := renderString(t, components.UsersTable(view))

	if !strings.Contains(out, `role="alert"`) {
		t.Error("UsersTable() with FormError set missing role=alert")
	}
	if !strings.Contains(out, view.FormError) {
		t.Errorf("UsersTable() missing the FormError text: %s", out)
	}

	noError := renderString(t, components.UsersTable(testUsersView()))
	if strings.Contains(noError, `role="alert"`) {
		t.Error("UsersTable() renders the alert region even when FormError is empty")
	}
}

func TestUsersTable_AddUserForm_PrefillsAfterFailedSubmission(t *testing.T) {
	view := testUsersView()
	view.NewDisplayName = "Maya"
	view.NewEmail = "maya@example.com"
	out := renderString(t, components.UsersTable(view))

	if !strings.Contains(out, `value="Maya"`) {
		t.Error("add-user form did not round-trip NewDisplayName")
	}
	if !strings.Contains(out, `value="maya@example.com"`) {
		t.Error("add-user form did not round-trip NewEmail")
	}
}

// TestUsersTable_ActiveRow_DeactivateFormWiredCorrectly covers an active
// user: a Deactivate button, no "Deactivated" badge, and the row's forms
// posting to /admin/users/{id}/role and /admin/users/{id}/deactivate.
func TestUsersTable_ActiveRow_DeactivateFormWiredCorrectly(t *testing.T) {
	const id = "22222222-2222-7222-8222-222222222222"
	view := testUsersView(components.UserRowView{
		ID: id, DisplayName: "Daniel", Email: "daniel@example.com",
		Role: "member", Active: true, Owner: components.OwnerView{Name: "Daniel", Initials: "D", Color: components.OwnerSteel},
	})
	out := renderString(t, components.UsersTable(view))

	if strings.Contains(out, "Deactivated") {
		t.Error("an active row must not show the Deactivated badge")
	}
	if !strings.Contains(out, ">Deactivate<") {
		t.Errorf("active row missing the Deactivate button: %s", out)
	}
	if !strings.Contains(out, `hx-post="/admin/users/`+id+`/role"`) {
		t.Errorf("role form not posting to the expected route: %s", out)
	}
	if !strings.Contains(out, `hx-post="/admin/users/`+id+`/deactivate"`) {
		t.Errorf("active row's action form not posting to /deactivate: %s", out)
	}
	if !strings.Contains(out, `hx-post="/admin/users/`+id+`/password"`) {
		t.Errorf("password form not posting to the expected route: %s", out)
	}
	if !strings.Contains(out, `hx-target="#user-list"`) || !strings.Contains(out, `hx-swap="outerHTML"`) {
		t.Errorf("row forms missing the outerHTML swap wiring into #user-list: %s", out)
	}
	if strings.Contains(out, "opacity-60") {
		t.Error("an active row must not be muted")
	}
}

// TestUsersTable_DeactivatedRow_ReactivateFormAndBadge covers a deactivated
// user: the Deactivated badge, a Reactivate button, the row muted, and the
// display name/avatar still rendered — the property that keeps history
// entries resolvable.
func TestUsersTable_DeactivatedRow_ReactivateFormAndBadge(t *testing.T) {
	const id = "33333333-3333-7333-8333-333333333333"
	view := testUsersView(components.UserRowView{
		ID: id, DisplayName: "Ivy", Email: "ivy@example.com",
		Role: "member", Active: false, Owner: components.OwnerView{Name: "Ivy", Initials: "I", Color: components.OwnerTeal},
	})
	out := renderString(t, components.UsersTable(view))

	if !strings.Contains(out, "Deactivated") {
		t.Error("a deactivated row must show the Deactivated badge")
	}
	if !strings.Contains(out, ">Reactivate<") {
		t.Errorf("deactivated row missing the Reactivate button: %s", out)
	}
	if !strings.Contains(out, `hx-post="/admin/users/`+id+`/reactivate"`) {
		t.Errorf("deactivated row's action form not posting to /reactivate: %s", out)
	}
	if !strings.Contains(out, "opacity-60") {
		t.Error("a deactivated row must render muted")
	}
	if !strings.Contains(out, "Ivy") {
		t.Error("a deactivated row must still render the display name")
	}
}

// TestUsersTable_RoleSelect_PreSelectsCurrentRole verifies the per-row role
// <select> marks the user's current role as selected — both roles are
// covered so the boolean attribute expression (selected?={...}) is
// exercised for both branches.
func TestUsersTable_RoleSelect_PreSelectsCurrentRole(t *testing.T) {
	tests := []struct {
		role string
	}{{"admin"}, {"member"}}
	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			view := testUsersView(components.UserRowView{
				ID: "44444444-4444-7444-8444-444444444444", DisplayName: "Leo", Email: "leo@example.com",
				Role: tt.role, Active: true, Owner: components.OwnerView{Name: "Leo", Initials: "L", Color: components.OwnerPeri},
			})
			out := renderString(t, components.UsersTable(view))
			if !strings.Contains(out, `value="`+tt.role+`" selected>`) {
				t.Errorf("role select did not mark %q as selected: %s", tt.role, out)
			}
		})
	}
}
