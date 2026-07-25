package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ericfisherdev/nestorage/internal/platform/config"
)

// baseEmailEnv reuses baseMediaEnv (media_test.go): both are just "the
// minimal environment Load needs to succeed" with no email-specific
// variable set, so a fresh baseline exercises EmailConfig's own zero-config
// defaults.
func baseEmailEnv() map[string]string {
	return baseMediaEnv()
}

func TestLoad_EmailDefaults(t *testing.T) {
	setEnv(t, baseEmailEnv())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Email.Enabled {
		t.Error("Email.Enabled = true, want false — a fresh install must default to the no-op sender")
	}
	if cfg.Email.DispatchInterval != 30*time.Second {
		t.Errorf("Email.DispatchInterval = %v, want the default 30s", cfg.Email.DispatchInterval)
	}
	if cfg.Email.DispatchBatchSize != 20 {
		t.Errorf("Email.DispatchBatchSize = %d, want the default 20", cfg.Email.DispatchBatchSize)
	}
	if cfg.Email.MaxAttempts != 5 {
		t.Errorf("Email.MaxAttempts = %d, want the default 5", cfg.Email.MaxAttempts)
	}
	if cfg.Email.FromAddress != "" || cfg.Email.SESRegion != "" {
		t.Errorf("Email.FromAddress/SESRegion = %q/%q, want both empty when unset and disabled", cfg.Email.FromAddress, cfg.Email.SESRegion)
	}
}

// TestLoad_EmailDispatchFieldsAlwaysValidated_EvenDisabled covers the
// deliberate asymmetry in EmailConfig.Validate: the dispatcher runs
// unconditionally regardless of EMAIL_ENABLED, so its own polling/retry
// fields must be valid even on a disabled install — unlike FromAddress/
// SESRegion, which are gated.
func TestLoad_EmailDispatchFieldsAlwaysValidated_EvenDisabled(t *testing.T) {
	env := baseEmailEnv()
	env["EMAIL_DISPATCH_INTERVAL"] = "0s"
	setEnv(t, env)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "EMAIL_DISPATCH_INTERVAL") {
		t.Fatalf("Load() error = %v, want an error naming EMAIL_DISPATCH_INTERVAL even though EMAIL_ENABLED is unset", err)
	}
}

func TestLoad_EmailDispatchBatchSizeNonPositiveRejected(t *testing.T) {
	env := baseEmailEnv()
	env["EMAIL_DISPATCH_BATCH_SIZE"] = "0"
	setEnv(t, env)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "EMAIL_DISPATCH_BATCH_SIZE") {
		t.Fatalf("Load() error = %v, want an error naming EMAIL_DISPATCH_BATCH_SIZE", err)
	}
}

func TestLoad_EmailMaxAttemptsNonPositiveRejected(t *testing.T) {
	env := baseEmailEnv()
	env["EMAIL_MAX_ATTEMPTS"] = "-1"
	setEnv(t, env)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "EMAIL_MAX_ATTEMPTS") {
		t.Fatalf("Load() error = %v, want an error naming EMAIL_MAX_ATTEMPTS", err)
	}
}

func TestLoad_EmailDispatchOverrides(t *testing.T) {
	env := baseEmailEnv()
	env["EMAIL_DISPATCH_INTERVAL"] = "1m"
	env["EMAIL_DISPATCH_BATCH_SIZE"] = "50"
	env["EMAIL_MAX_ATTEMPTS"] = "10"
	setEnv(t, env)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Email.DispatchInterval != time.Minute {
		t.Errorf("Email.DispatchInterval = %v, want 1m", cfg.Email.DispatchInterval)
	}
	if cfg.Email.DispatchBatchSize != 50 {
		t.Errorf("Email.DispatchBatchSize = %d, want 50", cfg.Email.DispatchBatchSize)
	}
	if cfg.Email.MaxAttempts != 10 {
		t.Errorf("Email.MaxAttempts = %d, want 10", cfg.Email.MaxAttempts)
	}
}

// TestLoad_EmailDisabledIgnoresPartialSESVars covers the caller-gating
// contract: a stray/partial SES_* setting left over from a copy-pasted
// .env must NOT fail a disabled Load, mirroring
// TestLoad_MediaStorageBackendLocalIgnoresPartialS3Vars.
func TestLoad_EmailDisabledIgnoresPartialSESVars(t *testing.T) {
	env := baseEmailEnv()
	env["SES_ACCESS_KEY_ID"] = "only-the-id-set"
	setEnv(t, env)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil (SES_* is ignored while EMAIL_ENABLED is false)", err)
	}
	if cfg.Email.Enabled {
		t.Error("Email.Enabled = true, want false")
	}
}

// TestLoad_EmailEnabledRequiresFromAddressAndRegion covers the gated
// provider block: selecting EMAIL_ENABLED=true without EMAIL_FROM_ADDRESS/
// SES_REGION must fail Load naming both.
func TestLoad_EmailEnabledRequiresFromAddressAndRegion(t *testing.T) {
	env := baseEmailEnv()
	env["EMAIL_ENABLED"] = "true"
	setEnv(t, env)

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want an error naming the missing EMAIL_FROM_ADDRESS/SES_REGION")
	}
	if !strings.Contains(err.Error(), "EMAIL_FROM_ADDRESS") {
		t.Errorf("Load() error = %v, want it to name EMAIL_FROM_ADDRESS", err)
	}
	if !strings.Contains(err.Error(), "SES_REGION") {
		t.Errorf("Load() error = %v, want it to name SES_REGION", err)
	}
}

func TestLoad_EmailEnabledCredentialPairMismatchRejected(t *testing.T) {
	env := baseEmailEnv()
	env["EMAIL_ENABLED"] = "true"
	env["EMAIL_FROM_ADDRESS"] = "notifications@example.test"
	env["SES_REGION"] = "us-east-1"
	env["SES_ACCESS_KEY_ID"] = "only-the-id-set"
	setEnv(t, env)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "SES_ACCESS_KEY_ID") {
		t.Fatalf("Load() error = %v, want an error naming the mismatched SES credential pair", err)
	}
}

func TestLoad_EmailEnabledValid(t *testing.T) {
	env := baseEmailEnv()
	env["EMAIL_ENABLED"] = "true"
	env["EMAIL_FROM_ADDRESS"] = "notifications@example.test"
	env["SES_REGION"] = "us-east-1"
	env["SES_ACCESS_KEY_ID"] = "test"
	env["SES_SECRET_ACCESS_KEY"] = "testtest"
	setEnv(t, env)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if !cfg.Email.Enabled {
		t.Error("Email.Enabled = false, want true")
	}
	if cfg.Email.FromAddress != "notifications@example.test" {
		t.Errorf("Email.FromAddress = %q, want %q", cfg.Email.FromAddress, "notifications@example.test")
	}
	if cfg.Email.SESRegion != "us-east-1" {
		t.Errorf("Email.SESRegion = %q, want %q", cfg.Email.SESRegion, "us-east-1")
	}
	if cfg.Email.SESAccessKeyID != "test" || cfg.Email.SESSecretAccessKey != "testtest" {
		t.Errorf("Email.SESAccessKeyID/SESSecretAccessKey = %q/%q, want test/testtest", cfg.Email.SESAccessKeyID, cfg.Email.SESSecretAccessKey)
	}
}
