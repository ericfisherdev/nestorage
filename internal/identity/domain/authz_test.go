package domain_test

import (
	"testing"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// stubBin is a minimal domain.BinSubject fake — NSTR-27's real
// bins/domain.Bin does not exist yet, and this package must never import it
// even once it does (see BinSubject's own doc).
type stubBin struct {
	creator domain.UserID
	private bool
}

func (b stubBin) BinCreator() domain.UserID { return b.creator }
func (b stubBin) BinPrivate() bool          { return b.private }

// TestCanSeeBinAndCanMutateBin covers the full matrix the ticket asks for:
// {admin, creator, non-creator member, integration, anonymous} x {public,
// private} — for both CanSeeBin and CanMutateBin, which share today's rule
// (see CanMutateBin's own doc).
func TestCanSeeBinAndCanMutateBin(t *testing.T) {
	creator := domain.NewUserID()
	other := domain.NewUserID()

	admin := domain.NewUserPrincipal(other, domain.RoleAdmin, "Admin")
	creatorPrincipal := domain.NewUserPrincipal(creator, domain.RoleMember, "Creator")
	nonCreatorMember := domain.NewUserPrincipal(other, domain.RoleMember, "Member")
	integration := domain.NewIntegrationPrincipal("Nestova")
	anonymous := domain.Principal{}

	publicBin := stubBin{creator: creator, private: false}
	privateBin := stubBin{creator: creator, private: true}

	tests := []struct {
		name string
		p    domain.Principal
		bin  stubBin
		want bool
	}{
		{"admin sees public", admin, publicBin, true},
		{"admin sees private", admin, privateBin, true},
		{"creator sees public", creatorPrincipal, publicBin, true},
		{"creator sees own private", creatorPrincipal, privateBin, true},
		{"non-creator member sees public", nonCreatorMember, publicBin, true},
		{"non-creator member cannot see private", nonCreatorMember, privateBin, false},
		{"integration sees public", integration, publicBin, true},
		{"integration cannot see private — no creator identity, not an admin", integration, privateBin, false},
		{"anonymous sees public", anonymous, publicBin, true},
		{"anonymous cannot see private", anonymous, privateBin, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.CanSeeBin(tt.p, tt.bin); got != tt.want {
				t.Errorf("CanSeeBin() = %v, want %v", got, tt.want)
			}
			if got := domain.CanMutateBin(tt.p, tt.bin); got != tt.want {
				t.Errorf("CanMutateBin() = %v, want %v", got, tt.want)
			}
		})
	}
}
