package domain_test

import (
	"errors"
	"strings"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

func TestParseVisibility(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    domain.Visibility
		wantErr error
	}{
		{"public", "public", domain.VisibilityPublic, nil},
		{"private", "private", domain.VisibilityPrivate, nil},
		{"unknown value rejected", "shared", "", domain.ErrInvalidVisibility},
		{"blank rejected", "", "", domain.ErrInvalidVisibility},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.ParseVisibility(tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParseVisibility(%q) error = %v, want %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseVisibility(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestVisibility_IsPrivate(t *testing.T) {
	if domain.VisibilityPublic.IsPrivate() {
		t.Error("VisibilityPublic.IsPrivate() = true, want false")
	}
	if !domain.VisibilityPrivate.IsPrivate() {
		t.Error("VisibilityPrivate.IsPrivate() = false, want true")
	}
}

func TestNormalizeBinCode(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"trims and upper-cases", "  a1  ", "A1"},
		{"already normalized", "B2", "B2"},
		{"mixed case with punctuation", "gArAgE-1", "GARAGE-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.NormalizeBinCode(tt.input); got != tt.want {
				t.Errorf("NormalizeBinCode(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// validBin returns a Bin that passes Validate, so each Bin_Validate subtest
// can mutate exactly one field away from valid and confirm that field alone
// is what Validate rejects.
func validBin() *domain.Bin {
	return &domain.Bin{
		ID:         domain.NewBinID(),
		Code:       "A1",
		Name:       "Camping bin",
		LocationID: domain.NewLocationID(),
		CreatedBy:  identity.NewUserID(),
		Visibility: domain.VisibilityPublic,
	}
}

func TestBin_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*domain.Bin)
		wantErr error
	}{
		{"valid bin accepted", func(*domain.Bin) {}, nil},
		{"zero id rejected", func(b *domain.Bin) { b.ID = domain.BinID{} }, domain.ErrInvalidBin},
		{"blank code rejected", func(b *domain.Bin) { b.Code = "   " }, domain.ErrInvalidBin},
		{"over-long code rejected", func(b *domain.Bin) { b.Code = strings.Repeat("A", 33) }, domain.ErrInvalidBin},
		{"blank name rejected", func(b *domain.Bin) { b.Name = "  " }, domain.ErrInvalidBin},
		{"zero location id rejected", func(b *domain.Bin) { b.LocationID = domain.LocationID{} }, domain.ErrInvalidBin},
		{"zero created by rejected", func(b *domain.Bin) { b.CreatedBy = identity.UserID{} }, domain.ErrInvalidBin},
		{"invalid visibility rejected", func(b *domain.Bin) { b.Visibility = "shared" }, domain.ErrInvalidBin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := validBin()
			tt.mutate(b)
			if err := b.Validate(); !errors.Is(err, tt.wantErr) {
				t.Errorf("Validate() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestBin_MoveTo_RelocatesBin exercises the success path: MoveTo to a
// different location overwrites LocationID and returns nil.
func TestBin_MoveTo_RelocatesBin(t *testing.T) {
	from := domain.NewLocationID()
	to := domain.NewLocationID()
	b := &domain.Bin{LocationID: from}

	if err := b.MoveTo(to); err != nil {
		t.Fatalf("MoveTo: %v", err)
	}
	if b.LocationID != to {
		t.Errorf("LocationID after MoveTo = %v, want %v", b.LocationID, to)
	}
}

// TestBin_MoveTo_NoopRejected is the no-op guard NSTR-30 requires: moving to
// the bin's own current location is rejected with ErrBinAlreadyInLocation
// and leaves LocationID untouched, regardless of which caller invokes it.
func TestBin_MoveTo_NoopRejected(t *testing.T) {
	loc := domain.NewLocationID()
	b := &domain.Bin{LocationID: loc}

	err := b.MoveTo(loc)
	if !errors.Is(err, domain.ErrBinAlreadyInLocation) {
		t.Errorf("MoveTo(current location) = %v, want ErrBinAlreadyInLocation", err)
	}
	if b.LocationID != loc {
		t.Errorf("rejected MoveTo must not change LocationID: got %v, want %v", b.LocationID, loc)
	}
}

// TestBin_SatisfiesBinSubject exercises the two accessors identity.BinSubject
// needs, through the interface itself rather than the concrete type, so a
// regression that breaks the compile-time assertion in bin.go would also
// fail here with a clearer message.
func TestBin_SatisfiesBinSubject(t *testing.T) {
	creator := identity.NewUserID()
	b := &domain.Bin{CreatedBy: creator, Visibility: domain.VisibilityPrivate}

	var subject identity.BinSubject = b
	if subject.BinCreator() != creator {
		t.Errorf("BinCreator() = %v, want %v", subject.BinCreator(), creator)
	}
	if !subject.BinPrivate() {
		t.Error("BinPrivate() = false, want true for a private bin")
	}

	b.Visibility = domain.VisibilityPublic
	if subject.BinPrivate() {
		t.Error("BinPrivate() = true, want false for a public bin")
	}
}

// sqlVisibilityMirror restates, in Go, the exact SQL WHERE fragment
// storage/adapter's BinRepository uses to scope FindVisibleByID,
// FindVisibleByCode, ListVisible, UpdateVisibility, and Delete:
//
//	visibility = 'public' OR created_by = $viewerID OR $viewerIsAdmin
//
// Comparing this against identity.CanSeeBin across the full principal matrix
// below is what keeps the hand-written SQL honest against the locked authz
// rule without needing a database for this check — the gated
// bin_postgres_gated_test.go suite separately proves the SQL text itself
// behaves this way against a real Postgres.
func sqlVisibilityMirror(viewer identity.Principal, b *domain.Bin) bool {
	if b.Visibility != domain.VisibilityPrivate {
		return true
	}
	if viewer.IsAdmin() {
		return true
	}
	return viewer.Kind == identity.KindUser && viewer.UserID == b.CreatedBy
}

func TestBinSQLPredicateAgreesWithCanSeeBin(t *testing.T) {
	creator := identity.NewUserID()
	other := identity.NewUserID()

	principals := []identity.Principal{
		identity.NewUserPrincipal(other, identity.RoleAdmin, "Admin"),
		identity.NewUserPrincipal(creator, identity.RoleMember, "Creator"),
		identity.NewUserPrincipal(other, identity.RoleMember, "Non-creator member"),
		identity.NewIntegrationPrincipal("Nestova"),
		{}, // anonymous
	}
	visibilities := []domain.Visibility{domain.VisibilityPublic, domain.VisibilityPrivate}

	for _, visibility := range visibilities {
		b := &domain.Bin{CreatedBy: creator, Visibility: visibility}
		for _, p := range principals {
			want := identity.CanSeeBin(p, b)
			got := sqlVisibilityMirror(p, b)
			if got != want {
				t.Errorf("visibility=%s principal=%+v: sqlVisibilityMirror = %v, CanSeeBin = %v",
					visibility, p, got, want)
			}
		}
	}
}
