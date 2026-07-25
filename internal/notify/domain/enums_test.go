package domain_test

import (
	"errors"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/notify/domain"
)

func TestChannel_ValidAndParse(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"in_app valid", "in_app", true},
		{"email valid", "email", true},
		{"unknown invalid", "sms", false},
		{"blank invalid", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := domain.Channel(tt.value)
			if got := c.Valid(); got != tt.want {
				t.Errorf("Channel(%q).Valid() = %v, want %v", tt.value, got, tt.want)
			}
			parsed, err := domain.ParseChannel(tt.value)
			if tt.want {
				if err != nil {
					t.Errorf("ParseChannel(%q) = %v, want nil error", tt.value, err)
				}
				if parsed.String() != tt.value {
					t.Errorf("ParseChannel(%q).String() = %q, want %q", tt.value, parsed.String(), tt.value)
				}
			} else if !errors.Is(err, domain.ErrInvalidChannel) {
				t.Errorf("ParseChannel(%q) = %v, want ErrInvalidChannel", tt.value, err)
			}
		})
	}
}

func TestStatus_ValidAndParse(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"pending valid", "pending", true},
		{"sent valid", "sent", true},
		{"failed valid", "failed", true},
		{"unknown invalid", "cancelled", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := domain.Status(tt.value)
			if got := s.Valid(); got != tt.want {
				t.Errorf("Status(%q).Valid() = %v, want %v", tt.value, got, tt.want)
			}
			parsed, err := domain.ParseStatus(tt.value)
			if tt.want {
				if err != nil {
					t.Errorf("ParseStatus(%q) = %v, want nil error", tt.value, err)
				}
				if parsed.String() != tt.value {
					t.Errorf("ParseStatus(%q).String() = %q, want %q", tt.value, parsed.String(), tt.value)
				}
			} else if !errors.Is(err, domain.ErrInvalidStatus) {
				t.Errorf("ParseStatus(%q) = %v, want ErrInvalidStatus", tt.value, err)
			}
		})
	}
}

func TestEventType_ValidAndParse(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"return_requested valid", "return_requested", true},
		{"item_returned valid", "item_returned", true},
		{"unknown invalid", "task_due_soon", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := domain.EventType(tt.value)
			if got := e.Valid(); got != tt.want {
				t.Errorf("EventType(%q).Valid() = %v, want %v", tt.value, got, tt.want)
			}
			parsed, err := domain.ParseEventType(tt.value)
			if tt.want {
				if err != nil {
					t.Errorf("ParseEventType(%q) = %v, want nil error", tt.value, err)
				}
				if parsed.String() != tt.value {
					t.Errorf("ParseEventType(%q).String() = %q, want %q", tt.value, parsed.String(), tt.value)
				}
			} else if !errors.Is(err, domain.ErrInvalidEventType) {
				t.Errorf("ParseEventType(%q) = %v, want ErrInvalidEventType", tt.value, err)
			}
		})
	}
}
