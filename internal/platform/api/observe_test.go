package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/ericfisherdev/nestorage/internal/platform/api"
)

// fixedKind returns a api.KindLabelFunc that ignores its context and always
// answers kind — Observe's own tests only need to prove the value flows
// through to the metric label and log line, not exercise real principal
// resolution (identity/adapter's own tests cover PrincipalKindLabel).
func fixedKind(kind string) api.KindLabelFunc {
	return func(_ context.Context) string { return kind }
}

func TestNewMetrics_NilRegistererPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewMetrics(nil) did not panic")
		}
	}()
	api.NewMetrics(nil)
}

func TestObserve_NilMetrics_Passthrough(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := api.Observe(nil, fixedKind("anonymous"), testLogger())(next)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/widgets", nil))

	if !called {
		t.Error("Observe(nil, ...) must still call next")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestObserve_RecordsRouteKindStatus(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := api.NewMetrics(reg)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Pattern = "GET /api/v1/widgets" // simulates the inner mux's own mutation.
		w.WriteHeader(http.StatusOK)
	})

	handler := api.Observe(m, fixedKind("user"), testLogger())(next)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/widgets", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	got := testutil.ToFloat64(m.RequestsTotal.WithLabelValues("GET /api/v1/widgets", "user", "200"))
	if got != 1 {
		t.Errorf("counter for (route=GET /api/v1/widgets, kind=user, status=200) = %v, want 1", got)
	}
}

// TestObserve_DeniedRequestStillCounted proves Observe records a request
// even when next denies it outright (e.g. RequireAPICredential's 401) before
// any inner route ever matched, so route falls back to the mount pattern
// (r.Pattern was never mutated) rather than being dropped from the metric
// entirely.
func TestObserve_DeniedRequestStillCounted(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := api.NewMetrics(reg)
	denied := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	handler := api.Observe(m, fixedKind("anonymous"), testLogger())(denied)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/widgets", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	got := testutil.ToFloat64(m.RequestsTotal.WithLabelValues("/api/v1/", "anonymous", "401"))
	if got != 1 {
		t.Errorf("counter for (route=/api/v1/, kind=anonymous, status=401) = %v, want 1", got)
	}
}

func TestObserve_KindNormalizedToBoundedSet(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := api.NewMetrics(reg)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, kind := range []string{"user", "integration", "anonymous"} {
		handler := api.Observe(m, fixedKind(kind), testLogger())(next)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/widgets", nil))
		if got := testutil.ToFloat64(m.RequestsTotal.WithLabelValues("/api/v1/", kind, "200")); got != 1 {
			t.Errorf("kind %q: counter = %v, want 1", kind, got)
		}
	}
}
