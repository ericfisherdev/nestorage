package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

func TestGenerateAPIKeySecret_PrefixedAndDistinct(t *testing.T) {
	first, err := domain.GenerateAPIKeySecret()
	if err != nil {
		t.Fatalf("GenerateAPIKeySecret: %v", err)
	}
	if !strings.HasPrefix(first, domain.APIKeyPrefix) {
		t.Errorf("GenerateAPIKeySecret() = %q, want prefix %q", first, domain.APIKeyPrefix)
	}
	second, err := domain.GenerateAPIKeySecret()
	if err != nil {
		t.Fatalf("GenerateAPIKeySecret: %v", err)
	}
	if first == second {
		t.Error("two calls to GenerateAPIKeySecret produced the same secret")
	}
}

// TestAPIKeyPrefix_DistinctFromDeviceTokenPrefix guards the NSTR-23
// reconciliation's explicit callout: "ns_" and "nsd_" must never be
// confusable by a prefix check, since NSTR-24's router dispatches on this
// distinction.
func TestAPIKeyPrefix_DistinctFromDeviceTokenPrefix(t *testing.T) {
	if strings.HasPrefix(domain.DeviceTokenPrefix, domain.APIKeyPrefix) {
		t.Errorf("DeviceTokenPrefix %q must not start with APIKeyPrefix %q", domain.DeviceTokenPrefix, domain.APIKeyPrefix)
	}
	if strings.HasPrefix(domain.APIKeyPrefix, domain.DeviceTokenPrefix) {
		t.Errorf("APIKeyPrefix %q must not start with DeviceTokenPrefix %q", domain.APIKeyPrefix, domain.DeviceTokenPrefix)
	}
}

func TestHashAPIKeySecret_Stable(t *testing.T) {
	raw, err := domain.GenerateAPIKeySecret()
	if err != nil {
		t.Fatalf("GenerateAPIKeySecret: %v", err)
	}
	first := domain.HashAPIKeySecret(raw)
	second := domain.HashAPIKeySecret(raw)
	if first != second {
		t.Errorf("HashAPIKeySecret is not stable across calls: %q != %q", first, second)
	}
}

func TestAPIKeySecretsMatch(t *testing.T) {
	raw, err := domain.GenerateAPIKeySecret()
	if err != nil {
		t.Fatalf("GenerateAPIKeySecret: %v", err)
	}
	hash := domain.HashAPIKeySecret(raw)

	if !domain.APIKeySecretsMatch(raw, hash) {
		t.Error("APIKeySecretsMatch(raw, hash) = false, want true")
	}
	if domain.APIKeySecretsMatch("wrong-secret", hash) {
		t.Error("APIKeySecretsMatch(wrong, hash) = true, want false")
	}
}

func TestKeyPrefixOf(t *testing.T) {
	raw := domain.APIKeyPrefix + strings.Repeat("a", 64)
	got := domain.KeyPrefixOf(raw)
	want := "ns_aaaaaaaa"
	if got != want {
		t.Errorf("KeyPrefixOf(%q) = %q, want %q", raw, got, want)
	}
}

func TestKeyPrefixOf_ShorterThanDisplayLenReturnsWholeInput(t *testing.T) {
	if got := domain.KeyPrefixOf("ns_ab"); got != "ns_ab" {
		t.Errorf("KeyPrefixOf(short) = %q, want the input unchanged", got)
	}
}

func TestNewAPIKeyID_DistinctAndParsable(t *testing.T) {
	a := domain.NewAPIKeyID()
	b := domain.NewAPIKeyID()
	if a == b {
		t.Error("two calls to NewAPIKeyID produced the same id")
	}
	parsed, err := domain.ParseAPIKeyID(a.String())
	if err != nil {
		t.Fatalf("ParseAPIKeyID: %v", err)
	}
	if parsed != a {
		t.Errorf("ParseAPIKeyID(a.String()) = %v, want %v", parsed, a)
	}
}

func TestParseAPIKeyID_Malformed(t *testing.T) {
	if _, err := domain.ParseAPIKeyID("not-a-uuid"); err == nil {
		t.Error("ParseAPIKeyID(malformed) = nil error, want an error")
	}
}

func newValidAPIKey() *domain.APIKey {
	return &domain.APIKey{
		ID:         domain.NewAPIKeyID(),
		KeyPrefix:  "ns_a1b2c3d4",
		SecretHash: "deadbeef",
		Label:      "Nestova integration",
	}
}

func TestAPIKey_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*domain.APIKey)
		wantErr bool
	}{
		{"valid", func(*domain.APIKey) {}, false},
		{"zero id", func(k *domain.APIKey) { k.ID = domain.APIKeyID{} }, true},
		{"blank key prefix", func(k *domain.APIKey) { k.KeyPrefix = "   " }, true},
		{"blank secret hash", func(k *domain.APIKey) { k.SecretHash = "" }, true},
		{"blank label", func(k *domain.APIKey) { k.Label = "  " }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := newValidAPIKey()
			tt.mutate(k)
			err := k.Validate()
			if tt.wantErr && !errors.Is(err, domain.ErrInvalidAPIKey) {
				t.Errorf("Validate() = %v, want ErrInvalidAPIKey", err)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestAPIKey_Usable(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	tests := []struct {
		name string
		key  *domain.APIKey
		want bool
	}{
		{"current (no expiry, no revoke)", &domain.APIKey{}, true},
		{"retiring (expiry in future)", &domain.APIKey{ExpiresAt: &future}, true},
		{"expired (expiry in past)", &domain.APIKey{ExpiresAt: &past}, false},
		{"revoked, no expiry", &domain.APIKey{RevokedAt: &past}, false},
		{"revoked, expiry in future", &domain.APIKey{ExpiresAt: &future, RevokedAt: &past}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.key.Usable(now); got != tt.want {
				t.Errorf("Usable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAPIKey_Status(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	tests := []struct {
		name string
		key  *domain.APIKey
		want domain.APIKeyStatus
	}{
		{"current", &domain.APIKey{}, domain.APIKeyStatusCurrent},
		{"retiring", &domain.APIKey{ExpiresAt: &future}, domain.APIKeyStatusRetiring},
		{"expired", &domain.APIKey{ExpiresAt: &past}, domain.APIKeyStatusExpired},
		{"revoked takes precedence over expired", &domain.APIKey{ExpiresAt: &past, RevokedAt: &past}, domain.APIKeyStatusRevoked},
		{"revoked takes precedence over current", &domain.APIKey{RevokedAt: &past}, domain.APIKeyStatusRevoked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.key.Status(now); got != tt.want {
				t.Errorf("Status() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAPIKeyStatus_String(t *testing.T) {
	if got := domain.APIKeyStatusCurrent.String(); got != "current" {
		t.Errorf("String() = %q, want %q", got, "current")
	}
}

func TestOverlapWindow_Valid(t *testing.T) {
	tests := []struct {
		w    domain.OverlapWindow
		want bool
	}{
		{domain.OverlapNone, true},
		{domain.Overlap24h, true},
		{domain.Overlap7d, true},
		{domain.OverlapWindow("30d"), false},
		{domain.OverlapWindow(""), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.w), func(t *testing.T) {
			if got := tt.w.Valid(); got != tt.want {
				t.Errorf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseOverlapWindow(t *testing.T) {
	got, err := domain.ParseOverlapWindow("24h")
	if err != nil {
		t.Fatalf("ParseOverlapWindow(\"24h\"): %v", err)
	}
	if got != domain.Overlap24h {
		t.Errorf("ParseOverlapWindow(\"24h\") = %q, want %q", got, domain.Overlap24h)
	}
}

func TestParseOverlapWindow_Invalid(t *testing.T) {
	_, err := domain.ParseOverlapWindow("30d")
	if !errors.Is(err, domain.ErrInvalidOverlapWindow) {
		t.Fatalf("ParseOverlapWindow(\"30d\") = %v, want ErrInvalidOverlapWindow", err)
	}
}

func TestOverlapWindow_Duration(t *testing.T) {
	tests := []struct {
		w    domain.OverlapWindow
		want time.Duration
	}{
		{domain.OverlapNone, 0},
		{domain.Overlap24h, 24 * time.Hour},
		{domain.Overlap7d, 7 * 24 * time.Hour},
		{domain.OverlapWindow("garbage"), 0},
	}
	for _, tt := range tests {
		t.Run(string(tt.w), func(t *testing.T) {
			if got := tt.w.Duration(); got != tt.want {
				t.Errorf("Duration() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOverlapWindow_String(t *testing.T) {
	if got := domain.Overlap7d.String(); got != "7d" {
		t.Errorf("String() = %q, want %q", got, "7d")
	}
}

func TestMaxOverlapWindow_MatchesLongestChoice(t *testing.T) {
	if domain.MaxOverlapWindow != domain.Overlap7d.Duration() {
		t.Errorf("MaxOverlapWindow = %v, want %v (Overlap7d's duration)", domain.MaxOverlapWindow, domain.Overlap7d.Duration())
	}
}
