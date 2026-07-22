package domain_test

import (
	"errors"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

func TestRoleParse(t *testing.T) {
	for _, s := range []string{"admin", "member"} {
		if r, err := domain.ParseRole(s); err != nil || r.String() != s {
			t.Errorf("ParseRole(%q) = (%q, %v), want valid", s, r, err)
		}
	}
	if _, err := domain.ParseRole("superuser"); err == nil {
		t.Error("ParseRole(superuser) = nil error, want error")
	} else if !errors.Is(err, domain.ErrInvalidRole) {
		t.Errorf("ParseRole(superuser) error = %v, want wrapped ErrInvalidRole", err)
	}
}

func TestRoleIsAdmin(t *testing.T) {
	if !domain.RoleAdmin.IsAdmin() {
		t.Error("RoleAdmin.IsAdmin() = false, want true")
	}
	if domain.RoleMember.IsAdmin() {
		t.Error("RoleMember.IsAdmin() = true, want false")
	}
}

func TestUserColorParse(t *testing.T) {
	for _, s := range []string{"indigo", "steel", "teal", "peri"} {
		if c, err := domain.ParseUserColor(s); err != nil || c.String() != s {
			t.Errorf("ParseUserColor(%q) = (%q, %v), want valid", s, c, err)
		}
	}
}

func TestUserColorParseRejectsShared(t *testing.T) {
	// "shared" is a valid owner palette key elsewhere (the Family/unowned
	// sentinel for bin ownership), but it is not a user-assignable color —
	// see UserColor's own doc.
	if _, err := domain.ParseUserColor("shared"); err == nil {
		t.Error("ParseUserColor(shared) = nil error, want error")
	} else if !errors.Is(err, domain.ErrInvalidColor) {
		t.Errorf("ParseUserColor(shared) error = %v, want wrapped ErrInvalidColor", err)
	}
}

func TestUserColorParseRejectsUnknown(t *testing.T) {
	if _, err := domain.ParseUserColor("chartreuse"); err == nil {
		t.Error("ParseUserColor(chartreuse) = nil error, want error")
	} else if !errors.Is(err, domain.ErrInvalidColor) {
		t.Errorf("ParseUserColor(chartreuse) error = %v, want wrapped ErrInvalidColor", err)
	}
}

func TestUserIDRoundTrip(t *testing.T) {
	id := domain.NewUserID()
	got, err := domain.ParseUserID(id.String())
	if err != nil || got != id {
		t.Errorf("user id round-trip = (%v, %v), want %v", got, err, id)
	}
	if _, err := domain.ParseUserID("not-a-uuid"); err == nil {
		t.Error("ParseUserID(not-a-uuid) = nil error, want error")
	}
}
