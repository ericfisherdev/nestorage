package domain_test

import (
	"testing"

	"github.com/ericfisherdev/nestorage/internal/notify/domain"
)

func TestNotificationID_RoundTrip(t *testing.T) {
	id := domain.NewNotificationID()
	if id.String() == "" {
		t.Fatal("NewNotificationID().String() is empty")
	}
	parsed, err := domain.ParseNotificationID(id.String())
	if err != nil {
		t.Fatalf("ParseNotificationID(%q): %v", id.String(), err)
	}
	if parsed != id {
		t.Errorf("ParseNotificationID round trip = %v, want %v", parsed, id)
	}
}

func TestNotificationID_TwoCallsDiffer(t *testing.T) {
	first := domain.NewNotificationID()
	second := domain.NewNotificationID()
	if first == second {
		t.Error("NewNotificationID() returned the same id twice")
	}
}

func TestParseNotificationID_Malformed(t *testing.T) {
	if _, err := domain.ParseNotificationID("not-a-uuid"); err == nil {
		t.Error("ParseNotificationID(malformed) = nil error, want an error")
	}
}
