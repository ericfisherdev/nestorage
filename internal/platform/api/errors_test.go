package api_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/platform/api"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestWriteJSON_EncodesBodyAndContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	api.WriteJSON(rec, testLogger(), 201, map[string]string{"ok": "yes"})

	if rec.Code != 201 {
		t.Errorf("status = %d, want %d", rec.Code, 201)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["ok"] != "yes" {
		t.Errorf("body = %+v, want {ok: yes}", body)
	}
}

// TestWriteJSON_EncodeFailureLogged proves an encode failure (an
// unmarshalable value) is logged rather than panicking or silently dropped.
func TestWriteJSON_EncodeFailureLogged(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	rec := httptest.NewRecorder()
	api.WriteJSON(rec, logger, 200, make(chan int)) // channels are never JSON-encodable.

	if !strings.Contains(buf.String(), "encode response") {
		t.Errorf("log output = %q, want it to record the encode failure", buf.String())
	}
}

func TestWriteError_EnvelopeShape(t *testing.T) {
	rec := httptest.NewRecorder()
	api.WriteError(rec, testLogger(), 400, api.CodeInvalidRequest, "invalid request",
		api.FieldDetail{Field: "device_name", Message: "must not be blank"})

	if rec.Code != 400 {
		t.Errorf("status = %d, want %d", rec.Code, 400)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details []struct {
				Field   string `json:"field"`
				Message string `json:"message"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error.Code != "invalid_request" {
		t.Errorf("code = %q, want %q", body.Error.Code, "invalid_request")
	}
	if body.Error.Message != "invalid request" {
		t.Errorf("message = %q, want %q", body.Error.Message, "invalid request")
	}
	if len(body.Error.Details) != 1 || body.Error.Details[0].Field != "device_name" {
		t.Errorf("details = %+v, want one field=device_name entry", body.Error.Details)
	}
}

func TestWriteError_NoDetails_OmitsDetailsField(t *testing.T) {
	rec := httptest.NewRecorder()
	api.WriteError(rec, testLogger(), 401, api.CodeUnauthorized, "unauthorized")

	if strings.Contains(rec.Body.String(), `"details"`) {
		t.Errorf("body = %q, want no details field when none was given (omitempty)", rec.Body.String())
	}
}
