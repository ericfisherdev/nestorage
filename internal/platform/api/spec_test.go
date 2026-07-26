package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/platform/api"
)

func TestNewSpecHandlers_NilLoggerPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewSpecHandlers(nil) did not panic")
		}
	}()
	api.NewSpecHandlers(nil)
}

// TestSpecHandlers_Routes_RegistersGETOpenAPIYAML proves Routes wires Serve
// up under the exact pattern newAppRoutes relies on (cmd/server/shell.go),
// independent of cmd/server/apispec_test.go's own route/spec sync check —
// this exercises registration through a real *http.ServeMux, package api's
// own coverage, rather than another package's test double.
func TestSpecHandlers_Routes_RegistersGETOpenAPIYAML(t *testing.T) {
	mux := http.NewServeMux()
	api.NewSpecHandlers(testLogger()).Routes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestSpecHandlers_Serve_WritesEmbeddedSpecAsYAML proves Serve answers with
// the embedded document verbatim, tagged application/yaml — the two things
// TestOpenAPISpec_ParsesAndValidates (spec_internal_test.go) does not check,
// since it reads openAPISpecYAML directly rather than going through Serve.
func TestSpecHandlers_Serve_WritesEmbeddedSpecAsYAML(t *testing.T) {
	rec := httptest.NewRecorder()
	api.NewSpecHandlers(testLogger()).Serve(rec, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", nil))

	if ct := rec.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/yaml")
	}
	if !strings.HasPrefix(rec.Body.String(), "openapi: 3.1") {
		t.Errorf("body = %.40q..., want it to start with the embedded document's own \"openapi: 3.1\" header", rec.Body.String())
	}
}
