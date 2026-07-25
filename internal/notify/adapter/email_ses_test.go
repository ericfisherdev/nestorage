package adapter

// Internal (white-box) test file: constructing an SESEmailSender directly
// with a test client needs its unexported fields, mirroring Nestova's own
// email_ses_test.go, whose fakeAWSJSONServer approach this file reuses —
// SES v2 has no open-source-emulatable equivalent to MinIO, so these tests
// point the sesv2 client at an in-process httptest.Server via the SDK's own
// BaseEndpoint override, serving hand-built responses that match the exact
// wire shape the SDK's generated deserializers expect.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"

	"github.com/ericfisherdev/nestorage/internal/notify/domain"
)

// newTestEmailSender builds an SESEmailSender whose client talks to server
// instead of real AWS, with retries disabled (aws.NopRetryer) so a test
// exercising a failure response completes in one round trip instead of
// waiting through the SDK's real backoff schedule.
func newTestEmailSender(server *httptest.Server) *SESEmailSender {
	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
		Retryer:     func() aws.Retryer { return aws.NopRetryer{} },
	}
	client := sesv2.NewFromConfig(cfg, func(o *sesv2.Options) {
		o.BaseEndpoint = aws.String(server.URL)
	})
	return &SESEmailSender{client: client, fromAddress: "sender@example.com"}
}

// fakeAWSJSONServer returns an httptest.Server that always responds with
// status/body, set to close automatically at the end of t.
func fakeAWSJSONServer(t *testing.T, status int, headers map[string]string, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSESEmailSender_Send_Success(t *testing.T) {
	srv := fakeAWSJSONServer(t, http.StatusOK, nil, `{"MessageId":"test-message-id-123"}`)
	sender := newTestEmailSender(srv)

	msg := domain.EmailMessage{To: "recipient@example.com", Subject: "Subject", TextBody: "text", HTMLBody: "<p>html</p>"}
	if err := sender.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestSESEmailSender_Send_FailureWrapped(t *testing.T) {
	srv := fakeAWSJSONServer(t, http.StatusInternalServerError,
		map[string]string{"X-Amzn-ErrorType": "InternalServiceErrorException"},
		`{"message":"internal error"}`)
	sender := newTestEmailSender(srv)

	msg := domain.EmailMessage{To: "recipient@example.com", Subject: "Subject", TextBody: "text", HTMLBody: "<p>html</p>"}
	err := sender.Send(context.Background(), msg)
	if err == nil {
		t.Fatal("Send(internal server error) must return an error")
	}
}

// TestSESEmailSender_Send_WiresSubjectAndBothBodies proves Send passes
// msg's fields through to the wire request exactly as given — SendEmailInput's
// Simple message shape (Subject, Body.Html, Body.Text) is already verified
// against the SDK's own types via context7; this confirms the adapter
// actually populates every one of those fields, not just some.
func TestSESEmailSender_Send_WiresSubjectAndBothBodies(t *testing.T) {
	var payload struct {
		FromEmailAddress string
		Destination      struct {
			ToAddresses []string
		}
		Content struct {
			Simple struct {
				Subject struct {
					Data string
				}
				Body struct {
					HTML struct {
						Data string
					}
					Text struct {
						Data string
					}
				}
			}
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"MessageId":"id"}`))
	}))
	t.Cleanup(srv.Close)
	sender := newTestEmailSender(srv)

	msg := domain.EmailMessage{To: "recipient@example.com", Subject: "Test Subject", TextBody: "text body", HTMLBody: "<p>HTML body</p>"}
	if err := sender.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if payload.FromEmailAddress != "sender@example.com" {
		t.Errorf("FromEmailAddress = %q, want %q", payload.FromEmailAddress, "sender@example.com")
	}
	if len(payload.Destination.ToAddresses) != 1 || payload.Destination.ToAddresses[0] != "recipient@example.com" {
		t.Errorf("Destination.ToAddresses = %v, want [recipient@example.com]", payload.Destination.ToAddresses)
	}
	if payload.Content.Simple.Subject.Data != "Test Subject" {
		t.Errorf("Subject.Data = %q, want %q", payload.Content.Simple.Subject.Data, "Test Subject")
	}
	if payload.Content.Simple.Body.HTML.Data != "<p>HTML body</p>" {
		t.Errorf("Body.HTML.Data = %q, want %q", payload.Content.Simple.Body.HTML.Data, "<p>HTML body</p>")
	}
	if payload.Content.Simple.Body.Text.Data != "text body" {
		t.Errorf("Body.Text.Data = %q, want %q", payload.Content.Simple.Body.Text.Data, "text body")
	}
}

// ---------------------------------------------------------------------------
// NewSESEmailSender — guard-clause validation, no AWS dependency (these all
// return before any network/credential call).
// ---------------------------------------------------------------------------

func TestNewSESEmailSender_RejectsInvalidParams(t *testing.T) {
	valid := SESEmailParams{
		Region:      "us-east-1",
		FromAddress: "sender@example.com",
	}

	tests := []struct {
		name    string
		mutate  func(p SESEmailParams) SESEmailParams
		wantErr string
	}{
		{
			name:    "blank region",
			mutate:  func(p SESEmailParams) SESEmailParams { p.Region = ""; return p },
			wantErr: "region",
		},
		{
			name:    "blank from address",
			mutate:  func(p SESEmailParams) SESEmailParams { p.FromAddress = ""; return p },
			wantErr: "from address",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewSESEmailSender(context.Background(), tt.mutate(valid))
			if err == nil {
				t.Fatal("NewSESEmailSender: want an error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("NewSESEmailSender error = %q, want it to mention %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestNewSESEmailSender_ValidParams_Succeeds(t *testing.T) {
	// Static credentials avoid the SDK reaching for IMDS/shared config during
	// LoadDefaultConfig, so this makes no network call.
	sender, err := NewSESEmailSender(context.Background(), SESEmailParams{
		Region: "us-east-1", FromAddress: "sender@example.com",
		AccessKeyID: "test", SecretAccessKey: "test",
	})
	if err != nil {
		t.Fatalf("NewSESEmailSender: %v", err)
	}
	if sender == nil {
		t.Fatal("NewSESEmailSender returned a nil sender with a nil error")
	}
}
