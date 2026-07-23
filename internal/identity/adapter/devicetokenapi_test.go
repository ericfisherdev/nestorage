package adapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// fakeDeviceTokenIssuer is a configurable deviceTokenIssuer fake: err makes
// Issue fail, and gotEmail/gotPassword/gotDeviceName capture the last call's
// arguments so a test can assert the request body was decoded correctly.
type fakeDeviceTokenIssuer struct {
	plaintext string
	token     *domain.DeviceToken
	err       error

	gotEmail, gotPassword, gotDeviceName string
}

func (f *fakeDeviceTokenIssuer) Issue(_ context.Context, email, password, deviceName string) (string, *domain.DeviceToken, error) {
	f.gotEmail, f.gotPassword, f.gotDeviceName = email, password, deviceName
	if f.err != nil {
		return "", nil, f.err
	}
	return f.plaintext, f.token, nil
}

func newAPIMux(issuer *fakeDeviceTokenIssuer) *http.ServeMux {
	mux := http.NewServeMux()
	adapter.NewDeviceTokenAPIHandlers(issuer, testLogger()).Routes(mux)
	return mux
}

func postJSON(t *testing.T, mux *http.ServeMux, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/device-tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestNewDeviceTokenAPIHandlers_NilDependenciesPanic(t *testing.T) {
	t.Run("nil issuer", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewDeviceTokenAPIHandlers(nil, logger) did not panic")
			}
		}()
		adapter.NewDeviceTokenAPIHandlers(nil, testLogger())
	})
	t.Run("nil logger", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewDeviceTokenAPIHandlers(issuer, nil) did not panic")
			}
		}()
		adapter.NewDeviceTokenAPIHandlers(&fakeDeviceTokenIssuer{}, nil)
	})
}

func TestDeviceTokenAPI_Issue_Success(t *testing.T) {
	userID := domain.NewUserID()
	token := &domain.DeviceToken{ID: domain.NewDeviceTokenID(), UserID: userID, Name: "Maya's phone"}
	issuer := &fakeDeviceTokenIssuer{plaintext: "nsd_deadbeef", token: token}
	mux := newAPIMux(issuer)

	rec := postJSON(t, mux, `{"email":"maya@example.com","password":"correct-horse","device_name":"Maya's phone"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if issuer.gotEmail != "maya@example.com" || issuer.gotPassword != "correct-horse" || issuer.gotDeviceName != "Maya's phone" {
		t.Errorf("Issue called with (%q, %q, %q), want the decoded request body", issuer.gotEmail, issuer.gotPassword, issuer.gotDeviceName)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["token"] != "nsd_deadbeef" {
		t.Errorf("response token = %v, want the plaintext", body["token"])
	}
	if body["id"] != token.ID.String() {
		t.Errorf("response id = %v, want %v", body["id"], token.ID.String())
	}
	if body["name"] != "Maya's phone" {
		t.Errorf("response name = %v, want %q", body["name"], "Maya's phone")
	}
	if _, ok := body["created_at"]; !ok {
		t.Error("response missing created_at")
	}
}

func TestDeviceTokenAPI_Issue_MalformedJSON(t *testing.T) {
	mux := newAPIMux(&fakeDeviceTokenIssuer{})
	rec := postJSON(t, mux, `{not json`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestDeviceTokenAPI_Issue_OversizedBodyRejected(t *testing.T) {
	issuer := &fakeDeviceTokenIssuer{}
	mux := newAPIMux(issuer)

	huge := `{"email":"a@example.com","password":"` + strings.Repeat("x", 2<<20) + `","device_name":"phone"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/device-tokens", bytes.NewReader([]byte(huge)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (MaxBytesReader must reject an oversized body)", rec.Code, http.StatusBadRequest)
	}
}

func TestDeviceTokenAPI_Issue_BlankDeviceNameRejected(t *testing.T) {
	issuer := &fakeDeviceTokenIssuer{err: domain.ErrInvalidDeviceToken}
	mux := newAPIMux(issuer)

	rec := postJSON(t, mux, `{"email":"maya@example.com","password":"correct-horse","device_name":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestDeviceTokenAPI_Issue_CredentialFailuresShareOneBody asserts every
// credential failure — wrong password and (simulated) unknown email alike —
// answers the exact same 401 body, so the endpoint cannot be used to
// enumerate accounts.
func TestDeviceTokenAPI_Issue_CredentialFailuresShareOneBody(t *testing.T) {
	wrongPassword := postJSON(t, newAPIMux(&fakeDeviceTokenIssuer{err: domain.ErrInvalidCredentials}),
		`{"email":"maya@example.com","password":"wrong","device_name":"phone"}`)
	unknownEmail := postJSON(t, newAPIMux(&fakeDeviceTokenIssuer{err: domain.ErrInvalidCredentials}),
		`{"email":"nobody@example.com","password":"anything","device_name":"phone"}`)

	if wrongPassword.Code != http.StatusUnauthorized || unknownEmail.Code != http.StatusUnauthorized {
		t.Fatalf("status codes = %d, %d, want both %d", wrongPassword.Code, unknownEmail.Code, http.StatusUnauthorized)
	}
	if wrongPassword.Body.String() != unknownEmail.Body.String() {
		t.Errorf("response bodies differ: %q vs %q, want byte-identical", wrongPassword.Body.String(), unknownEmail.Body.String())
	}
}

func TestDeviceTokenAPI_Issue_UnrecognizedErrorIs500(t *testing.T) {
	issuer := &fakeDeviceTokenIssuer{err: errors.New("boom")}
	mux := newAPIMux(issuer)

	rec := postJSON(t, mux, `{"email":"maya@example.com","password":"correct-horse","device_name":"phone"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestDeviceTokenAPI_Issue_MethodMismatch(t *testing.T) {
	mux := newAPIMux(&fakeDeviceTokenIssuer{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/device-tokens", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/v1/auth/device-tokens status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
