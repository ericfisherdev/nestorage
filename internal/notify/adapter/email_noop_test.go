package adapter_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/notify/adapter"
	"github.com/ericfisherdev/nestorage/internal/notify/domain"
)

func TestNewNoopEmailSender_NilLoggerPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewNoopEmailSender(nil) did not panic")
		}
	}()
	adapter.NewNoopEmailSender(nil)
}

func TestNoopEmailSender_Send_Succeeds(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	sender := adapter.NewNoopEmailSender(logger)

	msg := domain.EmailMessage{To: "someone@example.test", Subject: "hi", TextBody: "hello", HTMLBody: "<p>hello</p>"}
	if err := sender.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send() error = %v, want nil", err)
	}
}

func TestNoopEmailSender_SatisfiesEmailSenderPort(_ *testing.T) {
	var _ domain.EmailSender = adapter.NewNoopEmailSender(slog.New(slog.NewJSONHandler(io.Discard, nil)))
}
