package domain_test

import (
	"testing"

	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

func TestNewPhotoID_RoundTripsThroughParse(t *testing.T) {
	id := domain.NewPhotoID()
	if id == (domain.PhotoID{}) {
		t.Fatal("NewPhotoID returned the zero value")
	}

	parsed, err := domain.ParsePhotoID(id.String())
	if err != nil {
		t.Fatalf("ParsePhotoID(%q) error = %v, want nil", id.String(), err)
	}
	if parsed != id {
		t.Errorf("ParsePhotoID round trip = %v, want %v", parsed, id)
	}
}

func TestParsePhotoID_Invalid(t *testing.T) {
	if _, err := domain.ParsePhotoID("not-a-uuid"); err == nil {
		t.Error("ParsePhotoID(invalid) error = nil, want an error")
	}
}

func TestNewPhotoID_Unique(t *testing.T) {
	first := domain.NewPhotoID()
	second := domain.NewPhotoID()
	if first == second {
		t.Error("NewPhotoID returned the same id twice")
	}
}
