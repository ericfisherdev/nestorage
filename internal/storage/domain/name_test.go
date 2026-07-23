package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

func TestValidateLocationName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{"blank rejected", "", "", domain.ErrInvalidLocationName},
		{"whitespace-only rejected", "   \t  ", "", domain.ErrInvalidLocationName},
		{"normal name trimmed", "  Garage  ", "Garage", nil},
		{"already-trimmed name unchanged", "Hall closet", "Hall closet", nil},
		{"100 runes: maximum accepted", strings.Repeat("a", 100), strings.Repeat("a", 100), nil},
		{
			// Multi-byte runes must count once each, not once per byte —
			// len([]rune(...)) is what makes this pass; len(string) would
			// see more than 100 bytes and wrongly reject it.
			"100 multi-byte runes at the boundary",
			strings.Repeat("é", 100),
			strings.Repeat("é", 100),
			nil,
		},
		{"101 runes: too long", strings.Repeat("a", 101), "", domain.ErrInvalidLocationName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.ValidateLocationName(tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateLocationName(%q) error = %v, want %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ValidateLocationName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
