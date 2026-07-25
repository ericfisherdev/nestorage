package bootstrap_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/notify/adapter"
	"github.com/ericfisherdev/nestorage/internal/notify/bootstrap"
	"github.com/ericfisherdev/nestorage/internal/platform/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestNewEmailSender_Disabled_ReturnsNoop(t *testing.T) {
	sender, err := bootstrap.NewEmailSender(context.Background(), config.EmailConfig{Enabled: false}, testLogger())
	if err != nil {
		t.Fatalf("NewEmailSender: %v", err)
	}
	if _, ok := sender.(*adapter.NoopEmailSender); !ok {
		t.Errorf("NewEmailSender(disabled) = %T, want *adapter.NoopEmailSender — a fresh install must touch no AWS", sender)
	}
}

func TestNewEmailSender_Enabled_Valid_ReturnsSESSender(t *testing.T) {
	// Static credentials avoid the SDK reaching for IMDS/shared config
	// during construction, so this makes no network call.
	cfg := config.EmailConfig{
		Enabled: true, FromAddress: "notifications@example.test", SESRegion: "us-east-1",
		SESAccessKeyID: "test", SESSecretAccessKey: "test",
	}
	sender, err := bootstrap.NewEmailSender(context.Background(), cfg, testLogger())
	if err != nil {
		t.Fatalf("NewEmailSender: %v", err)
	}
	if _, ok := sender.(*adapter.SESEmailSender); !ok {
		t.Errorf("NewEmailSender(enabled) = %T, want *adapter.SESEmailSender", sender)
	}
}

func TestNewEmailSender_Enabled_IncompleteConfig_ErrorsAtStartup(t *testing.T) {
	cfg := config.EmailConfig{Enabled: true} // no FromAddress, no SESRegion
	_, err := bootstrap.NewEmailSender(context.Background(), cfg, testLogger())
	if err == nil {
		t.Fatal("NewEmailSender(enabled, incomplete config) = nil error, want an error at construction time")
	}
}
