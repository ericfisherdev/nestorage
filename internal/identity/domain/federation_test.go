package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

func TestValidateMemberID_Blank(t *testing.T) {
	for _, s := range []string{"", "   "} {
		err := domain.ValidateMemberID(s)
		if !errors.Is(err, domain.ErrInvalidMemberLink) {
			t.Errorf("ValidateMemberID(%q) error = %v, want wrapped ErrInvalidMemberLink", s, err)
		}
	}
}

func TestValidateMemberID_TooLong(t *testing.T) {
	tooLong := strings.Repeat("a", 201)
	if err := domain.ValidateMemberID(tooLong); !errors.Is(err, domain.ErrInvalidMemberLink) {
		t.Errorf("ValidateMemberID(201 chars) error = %v, want wrapped ErrInvalidMemberLink", err)
	}
}

func TestValidateMemberID_Valid(t *testing.T) {
	if err := domain.ValidateMemberID("nes-member-123"); err != nil {
		t.Errorf("ValidateMemberID(valid) = %v, want nil", err)
	}
	// Exactly at the boundary must still pass.
	if err := domain.ValidateMemberID(strings.Repeat("a", 200)); err != nil {
		t.Errorf("ValidateMemberID(200 chars) = %v, want nil", err)
	}
}

func TestValidateHouseholdID_Blank(t *testing.T) {
	if err := domain.ValidateHouseholdID(""); !errors.Is(err, domain.ErrInvalidMemberLink) {
		t.Errorf("ValidateHouseholdID(\"\") error = %v, want wrapped ErrInvalidMemberLink", err)
	}
}

func TestValidateHouseholdID_Valid(t *testing.T) {
	if err := domain.ValidateHouseholdID("household-abc"); err != nil {
		t.Errorf("ValidateHouseholdID(valid) = %v, want nil", err)
	}
}

func TestNextFederatedColor_EmptyUsersPicksFirstDeclared(t *testing.T) {
	got := domain.NextFederatedColor(nil)
	if got != domain.ColorIndigo {
		t.Errorf("NextFederatedColor(nil) = %v, want %v (first declared)", got, domain.ColorIndigo)
	}
}

func TestNextFederatedColor_PicksLeastUsed(t *testing.T) {
	users := []domain.User{
		{Color: domain.ColorIndigo},
		{Color: domain.ColorIndigo},
		{Color: domain.ColorSteel},
	}
	got := domain.NextFederatedColor(users)
	if got != domain.ColorTeal {
		t.Errorf("NextFederatedColor = %v, want %v (unused, first of the two unused in declaration order)", got, domain.ColorTeal)
	}
}

// TestNextFederatedColor_TiesBreakByDeclarationOrder asserts that when every
// color is equally used, the result is deterministic — federationColorOrder's
// own declaration order, not map iteration order.
func TestNextFederatedColor_TiesBreakByDeclarationOrder(t *testing.T) {
	users := []domain.User{
		{Color: domain.ColorIndigo},
		{Color: domain.ColorSteel},
		{Color: domain.ColorTeal},
		{Color: domain.ColorPeri},
	}
	for range 10 {
		got := domain.NextFederatedColor(users)
		if got != domain.ColorIndigo {
			t.Fatalf("NextFederatedColor(all tied) = %v, want %v (deterministic tie-break)", got, domain.ColorIndigo)
		}
	}
}

func TestMemberLinkID_RoundTrip(t *testing.T) {
	id := domain.NewMemberLinkID()
	got, err := domain.ParseMemberLinkID(id.String())
	if err != nil {
		t.Fatalf("ParseMemberLinkID: %v", err)
	}
	if got != id {
		t.Errorf("ParseMemberLinkID(id.String()) = %v, want %v", got, id)
	}
}

func TestParseMemberLinkID_Malformed(t *testing.T) {
	if _, err := domain.ParseMemberLinkID("not-a-uuid"); err == nil {
		t.Error("ParseMemberLinkID(malformed) = nil error, want an error")
	}
}
