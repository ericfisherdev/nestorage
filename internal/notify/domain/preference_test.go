package domain_test

import (
	"reflect"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/notify/domain"
)

func TestEffectiveChannels_NoStoredRows_InAppOnly(t *testing.T) {
	got := domain.EffectiveChannels(nil)
	want := []domain.Channel{domain.ChannelInApp}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EffectiveChannels(nil) = %v, want %v (never empty, in_app always present)", got, want)
	}
}

func TestEffectiveChannels_EmailEnabled_AddsEmail(t *testing.T) {
	userID := identity.NewUserID()
	stored := []domain.Preference{
		{UserID: userID, EventType: domain.EventTypeReturnRequested, Channel: domain.ChannelEmail, Enabled: true},
	}
	got := domain.EffectiveChannels(stored)
	want := []domain.Channel{domain.ChannelInApp, domain.ChannelEmail}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EffectiveChannels(email enabled) = %v, want %v", got, want)
	}
}

func TestEffectiveChannels_EmailDisabled_InAppOnly(t *testing.T) {
	userID := identity.NewUserID()
	stored := []domain.Preference{
		{UserID: userID, EventType: domain.EventTypeReturnRequested, Channel: domain.ChannelEmail, Enabled: false},
	}
	got := domain.EffectiveChannels(stored)
	want := []domain.Channel{domain.ChannelInApp}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EffectiveChannels(email disabled) = %v, want %v (in_app cannot be turned off, email skipped)", got, want)
	}
}

// TestEffectiveChannels_StoredInAppRow_NeverDuplicated is defense in depth:
// no known writer ever stores a ChannelInApp row (PreferenceService.
// SetEmailEnabled always writes ChannelEmail — see its own doc), but a
// stray one must not double up ChannelInApp in the result.
func TestEffectiveChannels_StoredInAppRow_NeverDuplicated(t *testing.T) {
	userID := identity.NewUserID()
	stored := []domain.Preference{
		{UserID: userID, EventType: domain.EventTypeReturnRequested, Channel: domain.ChannelInApp, Enabled: true},
	}
	got := domain.EffectiveChannels(stored)
	want := []domain.Channel{domain.ChannelInApp}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EffectiveChannels(stray in_app row) = %v, want %v (not duplicated)", got, want)
	}
}
