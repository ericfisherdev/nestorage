package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

func TestGenerateDeviceToken_CarriesThePrefix(t *testing.T) {
	token, err := domain.GenerateDeviceToken()
	if err != nil {
		t.Fatalf("GenerateDeviceToken: %v", err)
	}
	if !strings.HasPrefix(token, domain.DeviceTokenPrefix) {
		t.Errorf("GenerateDeviceToken() = %q, want it to start with %q", token, domain.DeviceTokenPrefix)
	}
}

func TestDeviceTokensMatch(t *testing.T) {
	token, err := domain.GenerateDeviceToken()
	if err != nil {
		t.Fatalf("GenerateDeviceToken: %v", err)
	}
	hash := domain.HashDeviceToken(token)

	if !domain.DeviceTokensMatch(token, hash) {
		t.Error("DeviceTokensMatch(token, hash) = false, want true")
	}
	if domain.DeviceTokensMatch("wrong-token", hash) {
		t.Error("DeviceTokensMatch(wrong, hash) = true, want false")
	}
}

func validDeviceToken() *domain.DeviceToken {
	return &domain.DeviceToken{
		ID:        domain.NewDeviceTokenID(),
		UserID:    domain.NewUserID(),
		TokenHash: "deadbeef",
		Name:      "Maya's phone",
	}
}

func TestDeviceTokenValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*domain.DeviceToken)
		wantErr error
	}{
		{"well-formed", func(*domain.DeviceToken) {}, nil},
		{"zero id", func(dt *domain.DeviceToken) { dt.ID = domain.DeviceTokenID{} }, domain.ErrInvalidDeviceToken},
		{"zero user id", func(dt *domain.DeviceToken) { dt.UserID = domain.UserID{} }, domain.ErrInvalidDeviceToken},
		{"blank name", func(dt *domain.DeviceToken) { dt.Name = "   " }, domain.ErrInvalidDeviceToken},
		{"blank token hash", func(dt *domain.DeviceToken) { dt.TokenHash = "" }, domain.ErrInvalidDeviceToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dt := validDeviceToken()
			tt.mutate(dt)
			err := dt.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Validate() = %v, want wrapped %v", err, tt.wantErr)
			}
		})
	}
}

func TestDeviceTokenActive(t *testing.T) {
	dt := validDeviceToken()
	if !dt.Active() {
		t.Error("a token with a nil RevokedAt must be Active")
	}
	revoked := time.Now()
	dt.RevokedAt = &revoked
	if dt.Active() {
		t.Error("a token with a non-nil RevokedAt must not be Active")
	}
}

func TestDeviceTokenIDRoundTrip(t *testing.T) {
	id := domain.NewDeviceTokenID()
	got, err := domain.ParseDeviceTokenID(id.String())
	if err != nil || got != id {
		t.Errorf("device token id round-trip = (%v, %v), want %v", got, err, id)
	}
	if _, err := domain.ParseDeviceTokenID("not-a-uuid"); err == nil {
		t.Error("ParseDeviceTokenID(not-a-uuid) = nil error, want error")
	}
}
