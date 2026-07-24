package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// TestNewItemLinkID_RoundTripsThroughParse mirrors media/domain's own
// TestNewPhotoID_RoundTripsThroughParse: NewItemLinkID/String/ParseItemLinkID
// are new code this ticket adds to ids.go, alongside the validation
// functions above.
func TestNewItemLinkID_RoundTripsThroughParse(t *testing.T) {
	id := domain.NewItemLinkID()
	if id == (domain.ItemLinkID{}) {
		t.Fatal("NewItemLinkID returned the zero value")
	}

	parsed, err := domain.ParseItemLinkID(id.String())
	if err != nil {
		t.Fatalf("ParseItemLinkID(%q) error = %v, want nil", id.String(), err)
	}
	if parsed != id {
		t.Errorf("ParseItemLinkID round trip = %v, want %v", parsed, id)
	}
}

func TestParseItemLinkID_Invalid(t *testing.T) {
	if _, err := domain.ParseItemLinkID("not-a-uuid"); err == nil {
		t.Error("ParseItemLinkID(invalid) error = nil, want an error")
	}
}

func TestNewItemLinkID_Unique(t *testing.T) {
	first := domain.NewItemLinkID()
	second := domain.NewItemLinkID()
	if first == second {
		t.Error("NewItemLinkID returned the same id twice")
	}
}

func TestValidateItemLinkLabel(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"blank rejected", "", domain.ErrItemLinkLabelRequired},
		{"whitespace-only rejected", "   \t  ", domain.ErrItemLinkLabelRequired},
		{"normal label accepted", "Owner's manual", nil},
		{"exactly max length accepted", strings.Repeat("a", domain.MaxItemLinkLabelRunes), nil},
		{"over max length rejected", strings.Repeat("a", domain.MaxItemLinkLabelRunes+1), domain.ErrItemLinkLabelTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := domain.ValidateItemLinkLabel(tt.input); !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateItemLinkLabel(%q) = %v, want %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidateItemLinkURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{"http accepted", "http://example.com/manual.pdf", "http://example.com/manual.pdf", nil},
		{"https accepted", "https://example.com/product?sku=123#specs", "https://example.com/product?sku=123#specs", nil},
		{"surrounding whitespace trimmed", "  https://example.com  ", "https://example.com", nil},
		{"javascript scheme rejected", "javascript:alert(1)", "", domain.ErrInvalidItemLinkURL},
		{"javascript scheme rejected case-insensitively", "JAVASCRIPT:alert(1)", "", domain.ErrInvalidItemLinkURL},
		{"data scheme rejected", "data:text/html,x", "", domain.ErrInvalidItemLinkURL},
		{"ftp scheme rejected", "ftp://example.com/file", "", domain.ErrInvalidItemLinkURL},
		{"protocol-relative rejected", "//host/path", "", domain.ErrInvalidItemLinkURL},
		{"schemeless rejected", "example.com", "", domain.ErrInvalidItemLinkURL},
		{"empty host rejected", "https://", "", domain.ErrInvalidItemLinkURL},
		{"blank rejected", "", "", domain.ErrInvalidItemLinkURL},
		{"malformed url rejected", "https://exa\tmple.com", "", domain.ErrInvalidItemLinkURL},
		{"over max length rejected", "https://example.com/" + strings.Repeat("a", domain.MaxItemLinkURLRunes), "", domain.ErrInvalidItemLinkURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.ValidateItemLinkURL(tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateItemLinkURL(%q) error = %v, want %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ValidateItemLinkURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
