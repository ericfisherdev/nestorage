package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

func TestNewReturnRequestID_RoundTripsThroughParse(t *testing.T) {
	id := domain.NewReturnRequestID()
	if id == (domain.ReturnRequestID{}) {
		t.Fatal("NewReturnRequestID returned the zero value")
	}

	parsed, err := domain.ParseReturnRequestID(id.String())
	if err != nil {
		t.Fatalf("ParseReturnRequestID(%q) error = %v, want nil", id.String(), err)
	}
	if parsed != id {
		t.Errorf("ParseReturnRequestID round trip = %v, want %v", parsed, id)
	}
}

func TestParseReturnRequestID_Invalid(t *testing.T) {
	if _, err := domain.ParseReturnRequestID("not-a-uuid"); err == nil {
		t.Error("ParseReturnRequestID(invalid) error = nil, want an error")
	}
}

func TestNewReturnRequestID_Unique(t *testing.T) {
	first := domain.NewReturnRequestID()
	second := domain.NewReturnRequestID()
	if first == second {
		t.Error("NewReturnRequestID returned the same id twice")
	}
}

func TestValidateReturnRequestMessage(t *testing.T) {
	blank := "   \t  "
	normal := "please, I need this back"
	maxLen := strings.Repeat("a", domain.MaxReturnRequestMessageRunes)
	overLen := strings.Repeat("a", domain.MaxReturnRequestMessageRunes+1)

	tests := []struct {
		name    string
		input   *string
		wantErr error
	}{
		{"nil accepted", nil, nil},
		{"blank rejected", &blank, domain.ErrInvalidReturnRequestMessage},
		{"normal message accepted", &normal, nil},
		{"exactly max length accepted", &maxLen, nil},
		{"over max length rejected", &overLen, domain.ErrInvalidReturnRequestMessage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := domain.ValidateReturnRequestMessage(tt.input); !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateReturnRequestMessage(%v) = %v, want %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestReturnRequestStatus_Valid(t *testing.T) {
	tests := []struct {
		status domain.ReturnRequestStatus
		want   bool
	}{
		{domain.ReturnRequestStatusOpen, true},
		{domain.ReturnRequestStatusFulfilled, true},
		{domain.ReturnRequestStatusCancelled, true},
		{domain.ReturnRequestStatus("bogus"), false},
		{domain.ReturnRequestStatus(""), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.Valid(); got != tt.want {
				t.Errorf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseReturnRequestStatus(t *testing.T) {
	got, err := domain.ParseReturnRequestStatus("open")
	if err != nil {
		t.Fatalf("ParseReturnRequestStatus(open) error = %v, want nil", err)
	}
	if got != domain.ReturnRequestStatusOpen {
		t.Errorf("ParseReturnRequestStatus(open) = %v, want %v", got, domain.ReturnRequestStatusOpen)
	}

	if _, err := domain.ParseReturnRequestStatus("bogus"); !errors.Is(err, domain.ErrInvalidReturnRequestStatus) {
		t.Errorf("ParseReturnRequestStatus(bogus) = %v, want ErrInvalidReturnRequestStatus", err)
	}
}

// openRequest returns a ReturnRequest in the open state, the starting point
// every Fulfill/Cancel guard test transitions away from.
func openRequest() *domain.ReturnRequest {
	return &domain.ReturnRequest{
		ID: domain.NewReturnRequestID(), ItemID: domain.NewItemID(),
		RequesterID: identity.NewUserID(), HolderID: identity.NewUserID(),
		Status: domain.ReturnRequestStatusOpen,
	}
}

func TestReturnRequest_Fulfill(t *testing.T) {
	t.Run("from open succeeds", func(t *testing.T) {
		r := openRequest()
		at := time.Now()
		if err := r.Fulfill(at); err != nil {
			t.Fatalf("Fulfill: %v", err)
		}
		if r.Status != domain.ReturnRequestStatusFulfilled {
			t.Errorf("Status = %v, want %v", r.Status, domain.ReturnRequestStatusFulfilled)
		}
		if r.ResolvedAt == nil || !r.ResolvedAt.Equal(at) {
			t.Errorf("ResolvedAt = %v, want %v", r.ResolvedAt, at)
		}
	})

	for _, status := range []domain.ReturnRequestStatus{domain.ReturnRequestStatusFulfilled, domain.ReturnRequestStatusCancelled} {
		t.Run("from "+string(status)+" rejected, unmodified", func(t *testing.T) {
			r := openRequest()
			r.Status = status
			original := r.Status

			if err := r.Fulfill(time.Now()); !errors.Is(err, domain.ErrReturnRequestNotOpen) {
				t.Errorf("Fulfill(%v) = %v, want ErrReturnRequestNotOpen", status, err)
			}
			if r.Status != original {
				t.Error("rejected Fulfill must leave Status unmodified")
			}
			if r.ResolvedAt != nil {
				t.Error("rejected Fulfill must leave ResolvedAt unmodified")
			}
		})
	}
}

func TestReturnRequest_Cancel(t *testing.T) {
	t.Run("from open succeeds", func(t *testing.T) {
		r := openRequest()
		at := time.Now()
		if err := r.Cancel(at); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		if r.Status != domain.ReturnRequestStatusCancelled {
			t.Errorf("Status = %v, want %v", r.Status, domain.ReturnRequestStatusCancelled)
		}
		if r.ResolvedAt == nil || !r.ResolvedAt.Equal(at) {
			t.Errorf("ResolvedAt = %v, want %v", r.ResolvedAt, at)
		}
	})

	for _, status := range []domain.ReturnRequestStatus{domain.ReturnRequestStatusFulfilled, domain.ReturnRequestStatusCancelled} {
		t.Run("from "+string(status)+" rejected, unmodified", func(t *testing.T) {
			r := openRequest()
			r.Status = status
			original := r.Status

			if err := r.Cancel(time.Now()); !errors.Is(err, domain.ErrReturnRequestNotOpen) {
				t.Errorf("Cancel(%v) = %v, want ErrReturnRequestNotOpen", status, err)
			}
			if r.Status != original {
				t.Error("rejected Cancel must leave Status unmodified")
			}
			if r.ResolvedAt != nil {
				t.Error("rejected Cancel must leave ResolvedAt unmodified")
			}
		})
	}
}
