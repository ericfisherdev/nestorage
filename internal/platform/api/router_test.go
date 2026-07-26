package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/platform/api"
)

// var _ api.Registrar = (*api.Router)(nil) proves Router satisfies the
// interface bounded-context adapters (and NSTR-57's recording test double)
// depend on, at compile time.
var _ api.Registrar = (*api.Router)(nil)

func TestNewRouter_NilLoggerPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewRouter(nil) did not panic")
		}
	}()
	api.NewRouter(nil)
}

func TestRouter_MatchedRoute_PassesThroughUntouched(t *testing.T) {
	rt := api.NewRouter(testLogger())
	rt.HandleFunc("GET /widgets/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom", "yes")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("id=" + r.PathValue("id")))
	})

	req := httptest.NewRequest(http.MethodGet, "/widgets/42", nil)
	rec := httptest.NewRecorder()
	rt.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if rec.Header().Get("X-Custom") != "yes" {
		t.Error("custom header from the real handler did not pass through untouched")
	}
	if rec.Body.String() != "id=42" {
		t.Errorf("body = %q, want %q (proves r.PathValue works through the router)", rec.Body.String(), "id=42")
	}
}

func TestRouter_UnmatchedPath_JSONNotFound(t *testing.T) {
	rt := api.NewRouter(testLogger())
	rt.HandleFunc("GET /widgets", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/no/such/route", nil)
	rec := httptest.NewRecorder()
	rt.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	assertEnvelopeCode(t, rec, "not_found")
}

func TestRouter_MethodMismatch_JSON405WithAllowHeader(t *testing.T) {
	rt := api.NewRouter(testLogger())
	rt.HandleFunc("POST /widgets", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })

	req := httptest.NewRequest(http.MethodGet, "/widgets", nil)
	rec := httptest.NewRecorder()
	rt.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if rec.Header().Get("Allow") != http.MethodPost {
		t.Errorf("Allow header = %q, want %q", rec.Header().Get("Allow"), http.MethodPost)
	}
	assertEnvelopeCode(t, rec, "method_not_allowed")
}

func TestRouter_UnmatchedPath_ContentTypeIsJSONNotPlainText(t *testing.T) {
	rt := api.NewRouter(testLogger())
	req := httptest.NewRequest(http.MethodGet, "/nothing-registered", nil)
	rec := httptest.NewRecorder()
	rt.Handler().ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q (net/http's own plain-text fallback must be suppressed)", ct, "application/json")
	}
}

// assertEnvelopeCode decodes rec's body as the shared envelope and asserts
// its error code.
func assertEnvelopeCode(t *testing.T, rec *httptest.ResponseRecorder, wantCode string) {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error.Code != wantCode {
		t.Errorf("code = %q, want %q", body.Error.Code, wantCode)
	}
}
