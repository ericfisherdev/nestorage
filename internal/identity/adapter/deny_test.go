package adapter_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
)

func TestNewDenier_NilLoggerPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewDenier(nil) did not panic")
		}
	}()
	adapter.NewDenier(nil)
}

func TestDenier_Deny_JSON(t *testing.T) {
	denier := adapter.NewDenier(testLogger())
	tests := []struct {
		name   string
		status int
		want   string
	}{
		{"401 under /api/", http.StatusUnauthorized, "unauthorized"},
		{"403 under /api/", http.StatusForbidden, "forbidden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertJSONDenial(t, denier, tt.status, tt.want)
		})
	}
}

// assertJSONDenial exercises one Deny call for a request under apiPathPrefix
// and checks the fixed JSON shape: status, Content-Type, the {"error":
// wantError} body, and that HX-Redirect is never set for a JSON response.
// Split out of TestDenier_Deny_JSON so its table-driven loop stays a thin
// dispatcher rather than nesting every assertion inside the t.Run closure.
func assertJSONDenial(t *testing.T, denier *adapter.Denier, status int, wantError string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/bins", nil)
	rec := httptest.NewRecorder()
	denier.Deny(rec, r, status)

	if rec.Code != status {
		t.Fatalf("status = %d, want %d", rec.Code, status)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error != wantError {
		t.Errorf("error = %q, want %q", body.Error, wantError)
	}
	if rec.Header().Get("HX-Redirect") != "" {
		t.Error("a JSON response must not carry HX-Redirect")
	}
}

func TestDenier_Deny_JSON_ViaAcceptHeader(t *testing.T) {
	// Not every JSON caller lives under apiPathPrefix — an explicit Accept
	// header must select the same shape.
	denier := adapter.NewDenier(testLogger())
	r := httptest.NewRequest(http.MethodGet, "/bins", nil)
	r.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	denier.Deny(rec, r, http.StatusUnauthorized)

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
}

func TestDenier_Deny_HTMX(t *testing.T) {
	denier := adapter.NewDenier(testLogger())

	t.Run("401 carries HX-Redirect to /login", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/bins", nil)
		r.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		denier.Deny(rec, r, http.StatusUnauthorized)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		if got := rec.Header().Get("HX-Redirect"); got != "/login" {
			t.Errorf("HX-Redirect = %q, want %q", got, "/login")
		}
	})

	t.Run("403 is bare, no HX-Redirect", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
		r.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		denier.Deny(rec, r, http.StatusForbidden)

		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
		}
		if got := rec.Header().Get("HX-Redirect"); got != "" {
			t.Errorf("HX-Redirect = %q, want empty — a 403 must not redirect an already-signed-in caller to /login", got)
		}
	})
}

func TestDenier_Deny_FullNavigation(t *testing.T) {
	denier := adapter.NewDenier(testLogger())

	t.Run("401 redirects to /login?next=<original path>", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/bins", nil)
		rec := httptest.NewRecorder()
		denier.Deny(rec, r, http.StatusUnauthorized)

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
		}
		if got := rec.Header().Get("Location"); got != "/login?next=%2Fbins" {
			t.Errorf("Location = %q, want %q", got, "/login?next=%2Fbins")
		}
	})

	t.Run("403 is plain text, not a redirect", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
		rec := httptest.NewRecorder()
		denier.Deny(rec, r, http.StatusForbidden)

		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
		}
		if rec.Header().Get("Location") != "" {
			t.Error("a 403 must never redirect")
		}
	})

	t.Run("an off-origin next is sanitized before reaching the redirect", func(t *testing.T) {
		// Proves Deny actually runs sanitizeNext on the constructed target
		// rather than embedding the raw RequestURI verbatim — sanitizeNext
		// itself is exhaustively covered by TestSanitizeNext
		// (web_internal_test.go); this only needs one representative case.
		r := httptest.NewRequest(http.MethodGet, "/%5Cevil.example/steal", nil)
		rec := httptest.NewRecorder()
		denier.Deny(rec, r, http.StatusUnauthorized)

		if got := rec.Header().Get("Location"); got != "/login?next=%2F" {
			t.Errorf("Location = %q, want the backslash-bearing target rejected back to root (%q)", got, "/login?next=%2F")
		}
	})
}

func TestDenier_Deny_BodiesAreFixedRegardlessOfWhichCheckFailed(t *testing.T) {
	// The no-leak criterion: nothing about the response names which of the
	// three credential checks failed, or whether the underlying resource
	// exists — Deny only ever sees a status code.
	denier := adapter.NewDenier(testLogger())
	r := httptest.NewRequest(http.MethodGet, "/api/v1/bins", nil)

	first := httptest.NewRecorder()
	denier.Deny(first, r, http.StatusUnauthorized)
	second := httptest.NewRecorder()
	denier.Deny(second, r, http.StatusUnauthorized)

	if first.Body.String() != second.Body.String() {
		t.Errorf("two 401 denials produced different bodies: %q vs %q", first.Body.String(), second.Body.String())
	}
}
