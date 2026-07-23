package domain_test

import (
	"errors"
	"strings"
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

func TestUserIsAdmin(t *testing.T) {
	admin := domain.User{Role: domain.RoleAdmin}
	if !admin.IsAdmin() {
		t.Error("User{Role: RoleAdmin}.IsAdmin() = false, want true")
	}
	member := domain.User{Role: domain.RoleMember}
	if member.IsAdmin() {
		t.Error("User{Role: RoleMember}.IsAdmin() = true, want false")
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

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantErr  error
	}{
		{"11 chars: too short", strings.Repeat("a", 11), domain.ErrPasswordTooShort},
		{"12 chars: minimum accepted", strings.Repeat("a", 12), nil},
		{"128 chars: maximum accepted", strings.Repeat("a", 128), nil},
		{"129 chars: too long", strings.Repeat("a", 129), domain.ErrPasswordTooLong},
		{
			// Multi-byte runes must count once each, not once per byte —
			// len([]rune(...)) is what makes this pass; len(string) would
			// see 24 bytes and wrongly accept it.
			"12 multi-byte runes at the boundary",
			strings.Repeat("é", 12),
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := domain.ValidatePassword(tt.password)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidatePassword(%d runes) = %v, want %v", len([]rune(tt.password)), err, tt.wantErr)
			}
		})
	}
}

func TestNormalizeEmail(t *testing.T) {
	tests := []struct {
		email string
		want  string
	}{
		{"  Maya@Example.com  ", "maya@example.com"},
		{"already@lower.case", "already@lower.case"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := domain.NormalizeEmail(tt.email); got != tt.want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", tt.email, got, tt.want)
		}
	}
}
