package metrics_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/ericfisherdev/nestorage/internal/platform/metrics"
)

func TestNewRateLimitMetrics_NilRegistererPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewRateLimitMetrics(nil, \"nestorage\") did not panic")
		}
	}()
	metrics.NewRateLimitMetrics(nil, "nestorage")
}

func TestNewRateLimitMetrics_EmptyNamespacePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewRateLimitMetrics(reg, \"\") did not panic")
		}
	}()
	metrics.NewRateLimitMetrics(prometheus.NewRegistry(), "")
}

func TestRateLimitMetrics_RecordLimited_IncrementsByPrincipalAndScope(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.NewRateLimitMetrics(reg, "nestorage")

	m.RecordLimited("user:alice", "api")
	m.RecordLimited("user:alice", "api")
	m.RecordLimited("anonymous", "auth")

	if got := testutil.ToFloat64(m.Limited.WithLabelValues("user:alice", "api")); got != 2 {
		t.Errorf("counter for (principal=user:alice, scope=api) = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.Limited.WithLabelValues("anonymous", "auth")); got != 1 {
		t.Errorf("counter for (principal=anonymous, scope=auth) = %v, want 1", got)
	}
}
