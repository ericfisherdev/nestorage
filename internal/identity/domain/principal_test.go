package domain_test

import (
	"errors"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

func TestParseKind(t *testing.T) {
	for _, s := range []string{"user", "integration"} {
		if k, err := domain.ParseKind(s); err != nil || k.String() != s {
			t.Errorf("ParseKind(%q) = (%q, %v), want valid", s, k, err)
		}
	}
	if _, err := domain.ParseKind("robot"); err == nil {
		t.Error("ParseKind(robot) = nil error, want error")
	} else if !errors.Is(err, domain.ErrInvalidKind) {
		t.Errorf("ParseKind(robot) error = %v, want wrapped ErrInvalidKind", err)
	}
}

func TestNewUserPrincipal(t *testing.T) {
	id := domain.NewUserID()
	p := domain.NewUserPrincipal(id, domain.RoleAdmin, "Maya")

	if p.Kind != domain.KindUser {
		t.Errorf("Kind = %v, want KindUser", p.Kind)
	}
	if p.UserID != id {
		t.Errorf("UserID = %v, want %v", p.UserID, id)
	}
	if p.Role != domain.RoleAdmin {
		t.Errorf("Role = %v, want RoleAdmin", p.Role)
	}
	if p.Actor() != "Maya" {
		t.Errorf("Actor() = %q, want %q", p.Actor(), "Maya")
	}
}

func TestNewIntegrationPrincipal_HardCodesMemberRole(t *testing.T) {
	p := domain.NewIntegrationPrincipal("Nestova")

	if p.Kind != domain.KindIntegration {
		t.Errorf("Kind = %v, want KindIntegration", p.Kind)
	}
	if p.Role != domain.RoleMember {
		t.Errorf("Role = %v, want RoleMember — there must be no way to build an admin integration", p.Role)
	}
	if p.UserID != (domain.UserID{}) {
		t.Errorf("UserID = %v, want the zero UserID", p.UserID)
	}
	if p.Actor() != "Nestova" {
		t.Errorf("Actor() = %q, want %q", p.Actor(), "Nestova")
	}
}

func TestPrincipalIsAdmin(t *testing.T) {
	tests := []struct {
		name string
		p    domain.Principal
		want bool
	}{
		{"user admin", domain.NewUserPrincipal(domain.NewUserID(), domain.RoleAdmin, "Maya"), true},
		{"user member", domain.NewUserPrincipal(domain.NewUserID(), domain.RoleMember, "Daniel"), false},
		{"integration with its real (member) role", domain.NewIntegrationPrincipal("Nestova"), false},
		{
			// NewIntegrationPrincipal never sets RoleAdmin, so this
			// constructs the invalid state directly to prove IsAdmin's
			// Kind check — not just Role — is what keeps an integration
			// out of an admin route.
			"integration with role forced to admin",
			domain.Principal{Kind: domain.KindIntegration, Role: domain.RoleAdmin, Label: "Nestova"},
			false,
		},
		{"anonymous", domain.Principal{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.IsAdmin(); got != tt.want {
				t.Errorf("IsAdmin() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPrincipalIsAnonymous(t *testing.T) {
	if !(domain.Principal{}).IsAnonymous() {
		t.Error("zero Principal.IsAnonymous() = false, want true")
	}
	if domain.NewUserPrincipal(domain.NewUserID(), domain.RoleMember, "Daniel").IsAnonymous() {
		t.Error("a real user Principal.IsAnonymous() = true, want false")
	}
}
