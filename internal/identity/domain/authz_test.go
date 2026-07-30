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
// {owner, creator, non-creator adult, non-creator child, integration,
// anonymous} x {public, private} — for both CanSeeBin and CanMutateBin, which
// share today's rule (see CanMutateBin's own doc). The child cases prove
// NSTR-116's rule that child is treated exactly as member-level (identical
// to adult) for now — any tighter restriction is explicitly future work.
func TestCanSeeBinAndCanMutateBin(t *testing.T) {
	creator := domain.NewUserID()
	other := domain.NewUserID()

	owner := domain.NewUserPrincipal(other, domain.RoleOwner, "Owner")
	creatorPrincipal := domain.NewUserPrincipal(creator, domain.RoleAdult, "Creator")
	nonCreatorAdult := domain.NewUserPrincipal(other, domain.RoleAdult, "Adult")
	nonCreatorChild := domain.NewUserPrincipal(other, domain.RoleChild, "Child")
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
		{"owner sees public", owner, publicBin, true},
		{"owner sees private", owner, privateBin, true},
		{"creator sees public", creatorPrincipal, publicBin, true},
		{"creator sees own private", creatorPrincipal, privateBin, true},
		{"non-creator adult sees public", nonCreatorAdult, publicBin, true},
		{"non-creator adult cannot see private", nonCreatorAdult, privateBin, false},
		{"non-creator child sees public", nonCreatorChild, publicBin, true},
		{"non-creator child cannot see private", nonCreatorChild, privateBin, false},
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
