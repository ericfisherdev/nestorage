package config_test

import (
	"strings"
	"testing"

	corecfg "github.com/ericfisherdev/nestcore/config"

	"github.com/ericfisherdev/nestorage/internal/platform/config"
)

// baseRateLimitEnv is the minimal environment Load needs to succeed, reused
// by every case below so each test only sets the RATE_LIMIT value it cares
// about.
func baseRateLimitEnv() map[string]string {
	return map[string]string{
		"APP_ENV":      corecfg.EnvDev,
		"DATABASE_URL": "postgres://u:p@example.com:5432/nestorage?sslmode=disable",
	}
}

// TestLoad_RateLimitDefaults covers NSTR-58's own AC that a fresh install
// needs zero RATE_LIMIT configuration to boot with rate limiting already
// active.
func TestLoad_RateLimitDefaults(t *testing.T) {
	setEnv(t, baseRateLimitEnv())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.RateLimit.APIRPS != 10 {
		t.Errorf("RateLimit.APIRPS = %d, want the default %d", cfg.RateLimit.APIRPS, 10)
	}
	if cfg.RateLimit.APIBurst != 30 {
		t.Errorf("RateLimit.APIBurst = %d, want the default %d", cfg.RateLimit.APIBurst, 30)
	}
	if cfg.RateLimit.AuthRPM != 10 {
		t.Errorf("RateLimit.AuthRPM = %d, want the default %d", cfg.RateLimit.AuthRPM, 10)
	}
	if cfg.RateLimit.AuthBurst != 5 {
		t.Errorf("RateLimit.AuthBurst = %d, want the default %d", cfg.RateLimit.AuthBurst, 5)
	}
}

func TestLoad_RateLimitOverrides(t *testing.T) {
	env := baseRateLimitEnv()
	env["API_RATE_LIMIT_RPS"] = "20"
	env["API_RATE_LIMIT_BURST"] = "60"
	env["AUTH_RATE_LIMIT_RPM"] = "3"
	env["AUTH_RATE_LIMIT_BURST"] = "2"
	setEnv(t, env)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.RateLimit.APIRPS != 20 {
		t.Errorf("RateLimit.APIRPS = %d, want 20", cfg.RateLimit.APIRPS)
	}
	if cfg.RateLimit.APIBurst != 60 {
		t.Errorf("RateLimit.APIBurst = %d, want 60", cfg.RateLimit.APIBurst)
	}
	if cfg.RateLimit.AuthRPM != 3 {
		t.Errorf("RateLimit.AuthRPM = %d, want 3", cfg.RateLimit.AuthRPM)
	}
	if cfg.RateLimit.AuthBurst != 2 {
		t.Errorf("RateLimit.AuthBurst = %d, want 2", cfg.RateLimit.AuthBurst)
	}
}

// TestLoad_RateLimitNonPositiveRejected covers every field's own positivity
// check, table-driven so each case's own env var is named in the resulting
// aggregated error.
func TestLoad_RateLimitNonPositiveRejected(t *testing.T) {
	tests := []struct {
		key string
	}{
		{key: "API_RATE_LIMIT_RPS"},
		{key: "API_RATE_LIMIT_BURST"},
		{key: "AUTH_RATE_LIMIT_RPM"},
		{key: "AUTH_RATE_LIMIT_BURST"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			env := baseRateLimitEnv()
			env[tt.key] = "0"
			setEnv(t, env)

			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("Load() error = %v, want an error naming %s", err, tt.key)
			}
		})
	}
}

// TestLoad_RateLimitUnparsableRejected covers each field's own parse-error
// path (corecfg.Int32 failing before Validate's positivity check ever
// runs), table-driven so every env var's own unparsable case is exercised.
func TestLoad_RateLimitUnparsableRejected(t *testing.T) {
	tests := []struct {
		key string
	}{
		{key: "API_RATE_LIMIT_RPS"},
		{key: "API_RATE_LIMIT_BURST"},
		{key: "AUTH_RATE_LIMIT_RPM"},
		{key: "AUTH_RATE_LIMIT_BURST"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			env := baseRateLimitEnv()
			env[tt.key] = "not-a-number"
			setEnv(t, env)

			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("Load() error = %v, want an error naming %s", err, tt.key)
			}
		})
	}
}
