package api

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// apiMetricsNamespace matches cmd/server/main.go's own metrics.NewHTTPMetrics
// namespace, so both metric families share one Prometheus scrape target
// with no name collision (nestorage_http_requests_total vs.
// nestorage_api_requests_total).
const apiMetricsNamespace = "nestorage"

// Metrics bundles the /api/v1-specific Prometheus instrumentation Observe
// records. Distinct from nestcore's own generic HTTPMetrics (already applied
// mux-wide, to every route): this one adds the principal-kind label AC 4
// requires — user vs. integration vs. anonymous API traffic — which the
// generic middleware has no way to know about.
type Metrics struct {
	// RequestsTotal counts completed /api/v1 requests, labelled by the
	// matched route pattern, the resolved principal's kind, and status code.
	RequestsTotal *prometheus.CounterVec
}

// NewMetrics constructs Metrics and registers it on reg. It panics when reg
// is nil, matching every other metrics constructor in this codebase (see
// nestcore/metrics.NewHTTPMetrics).
func NewMetrics(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		panic("platform/api: NewMetrics requires a non-nil registerer")
	}
	return &Metrics{
		RequestsTotal: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Namespace: apiMetricsNamespace,
			Name:      "api_requests_total",
			Help:      "Total number of /api/v1 requests served, by route, principal kind, and status code.",
		}, []string{"route", "principal_kind", "status"}),
	}
}

// KindLabelFunc reads the bounded-set principal-kind label ("user",
// "integration", or "anonymous") back out of a request context. Observe
// depends on this func rather than importing identity/adapter directly
// (DIP) — the composition root wires identityadapter.PrincipalKindLabel in.
type KindLabelFunc func(ctx context.Context) string

// Observe wraps next with per-/api/v1-request instrumentation: one counter
// increment and one structured log line, labelled by route, principal kind,
// and status. A nil m returns a passthrough middleware (matching nestcore's
// own middleware.Metrics(nil) convention), so tests and any bootstrap path
// without a registry can still compose this chain.
//
// Observe must sit OUTSIDE any credential gate (RequireAPICredential) so a
// denied request is still counted, with route recorded as the mount pattern
// since inner routing never ran.
//
// The route label is read from r.Pattern AFTER next has returned, from the
// same *http.Request value next was called with — never via a request copy
// derived through WithContext, which would both lose that write and break
// nestcore's own CaptureRoutePattern relay for these same routes (see that
// middleware's own doc for why).
func Observe(m *Metrics, kindOf KindLabelFunc, logger *slog.Logger) func(http.Handler) http.Handler {
	if m == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w}
			start := time.Now()
			next.ServeHTTP(rec, r)

			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			route := r.Pattern
			if route == "" {
				route = "/api/v1/"
			}
			kind := kindOf(r.Context())

			m.RequestsTotal.WithLabelValues(route, kind, strconv.Itoa(status)).Inc()
			logger.InfoContext(r.Context(), "api: request",
				"route", route, "principal_kind", kind, "status", status, "duration", time.Since(start))
		})
	}
}

// statusRecorder records the status code written through it while passing
// every write through unchanged. Unlike router.go's statusCapturer (which
// discards the body to substitute the JSON envelope), Observe only needs the
// final status for its metric label and log line, so the real body must
// still reach the client.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (rec *statusRecorder) WriteHeader(status int) {
	rec.status = status
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *statusRecorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.ResponseWriter.Write(b)
}
