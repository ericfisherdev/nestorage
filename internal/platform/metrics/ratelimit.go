// Package metrics owns NSTR-58's own rate-limit instrumentation: the one
// counter identityadapter.RateLimit records a denial through. It is
// Nestorage's own Prometheus-importing package for this specific metric —
// distinct from nestcore's generic metrics package (which knows nothing
// about rate limiting) and from platform/api's own Metrics (which counts
// every /api/v1 request, not specifically limited ones) — so
// identity/adapter's RateLimit middleware can depend on a narrow one-method
// port satisfied by this package's own type without pulling platform/api's
// broader dependency set into identity/adapter's rate-limiting files.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// RateLimitMetrics bundles NSTR-58's own rate-limit counter.
type RateLimitMetrics struct {
	// Limited counts every request identityadapter.RateLimit denied,
	// labelled by the resolved principal (or the fixed "anonymous" label —
	// see identityadapter.RateLimit's own doc for why anonymous traffic
	// never carries a raw, attacker-controlled IP as a label value) and
	// scope ("api" or "auth").
	Limited *prometheus.CounterVec
}

// NewRateLimitMetrics constructs RateLimitMetrics and registers it on reg,
// with the metric name prefixed by namespace (matching nestcore's own
// metrics.NewHTTPMetrics and platform/api.NewMetrics), so more than one
// application can share a scrape target without their rate-limit metrics
// colliding.
//
// It panics when reg is nil or namespace is empty, matching every other
// metrics constructor in this codebase.
func NewRateLimitMetrics(reg prometheus.Registerer, namespace string) *RateLimitMetrics {
	if reg == nil {
		panic("platform/metrics: NewRateLimitMetrics requires a non-nil registerer")
	}
	if namespace == "" {
		panic("platform/metrics: NewRateLimitMetrics requires a non-empty namespace")
	}
	return &RateLimitMetrics{
		Limited: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "api_rate_limited_requests_total",
			Help:      "Total number of /api/v1 requests denied by rate limiting, by principal and scope.",
		}, []string{"principal", "scope"}),
	}
}

// RecordLimited increments Limited for one denied request — the
// rateLimitRecorder port identityadapter.RateLimit depends on.
func (m *RateLimitMetrics) RecordLimited(principal, scope string) {
	m.Limited.WithLabelValues(principal, scope).Inc()
}
