package adapter

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ericfisherdev/nestcore/httpserver/middleware"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/api"
)

// rateLimitIPKeyPrefix marks a rate-limit key resolved by client IP rather
// than an authenticated principal. RecordLimited's own "principal" metric
// label collapses every such key to anonymousKindLabel (see
// rateLimitMetricPrincipal) rather than exposing the attacker-controlled
// address as a Prometheus label value, which would let a spray of distinct
// source IPs blow up the metric's own cardinality.
const rateLimitIPKeyPrefix = "ip:"

// integrationRateKey is PrincipalRateKey's own key for a KindIntegration
// principal — a fixed string since there is exactly one account api key per
// household (NSTR-23), so there is nothing further within that kind to
// distinguish.
const integrationRateKey = "integration"

// RateKeyFunc computes the bucket key RateLimit looks a request up by —
// PrincipalRateKey or ClientIPRateKey, the two the composition root wires
// in (cmd/server/shell.go).
type RateKeyFunc func(r *http.Request) string

// PrincipalRateKey is the RateKeyFunc the composition root wires into the
// account-wide API bucket: "user:<id>" for a KindUser principal, the fixed
// integrationRateKey for KindIntegration (there is exactly one account api
// key), and rateLimitIPKeyPrefix+addr (via middleware.ClientIP) for a
// request that resolved anonymous. Reading the client IP here is
// spoof-resistant: nestcore's ForwardedHeaders middleware — which only
// honors X-Forwarded-For from a request's peer when that peer is a
// TRUSTED_PROXIES entry — already ran earlier in httpserver.New's own
// canonical middleware order, before this ever reads it back.
func PrincipalRateKey(r *http.Request) string {
	p, ok := CurrentPrincipal(r.Context())
	if !ok {
		return rateLimitIPKeyPrefix + middleware.ClientIP(r.Context())
	}
	if p.Kind == domain.KindIntegration {
		return integrationRateKey
	}
	return "user:" + p.UserID.String()
}

// ClientIPRateKey is the RateKeyFunc the composition root wires into the
// stricter token-exchange bucket: always the caller's own resolved client
// IP. Those requests are unauthenticated by nature — the exchange endpoint
// IS the credential mint — so there is no principal yet to key by.
func ClientIPRateKey(r *http.Request) string {
	return rateLimitIPKeyPrefix + middleware.ClientIP(r.Context())
}

// rateLimitMetricPrincipal derives RecordLimited's own "principal" label
// from a RateKeyFunc's key: verbatim for a "user:<id>"/integrationRateKey
// key (bounded by household size), collapsed to anonymousKindLabel for any
// rateLimitIPKeyPrefix-prefixed key (attacker-controlled, unbounded) — see
// rateLimitIPKeyPrefix's own doc.
func rateLimitMetricPrincipal(key string) string {
	if strings.HasPrefix(key, rateLimitIPKeyPrefix) {
		return anonymousKindLabel
	}
	return key
}

// rateLimitAllower is the narrow port (ISP) RateLimit depends on, satisfied
// by *KeyedRateLimiter (a superset, via Allow).
type rateLimitAllower interface {
	Allow(key string, now time.Time) (retryAfter time.Duration, ok bool)
}

// rateLimitRecorder is the narrow port (ISP) RateLimit depends on for its
// own metric, satisfied by *platformmetrics.RateLimitMetrics (a superset,
// via RecordLimited) — this package cannot import internal/platform/metrics
// by name without an import cycle risk, so the port stays structural
// (Go's implicit interface satisfaction), matching deviceTokenIssuer's own
// cross-package shape.
type rateLimitRecorder interface {
	RecordLimited(principal, scope string)
}

// rateLimitErrorWriter is the narrow, func-typed port (DIP) RateLimit
// answers a 429 through, matching this package's own func-type-port idiom
// (api.KindLabelFunc, observe.go). The composition root binds this to a
// closure over api.WriteError — NSTR-53's shared JSON error writer — and
// the process logger, so the 429 body is the same documented envelope
// every other /api/v1 error in this codebase uses, coded
// api.CodeRateLimited.
type rateLimitErrorWriter func(w http.ResponseWriter, status int, code api.Code, message string)

// rateLimitedMessage is the fixed, detail-free body every 429 carries,
// mirroring Denier's own unauthorizedMessage/forbiddenMessage convention —
// nothing here needs to say more than the code and the Retry-After header
// already communicate.
const rateLimitedMessage = "too many requests"

// RateLimit gates next behind limiter, keyed per-request by key and scoped
// by the fixed scope label ("api" or "auth") RecordLimited's own metric
// carries. Every dependency is required; a nil one panics at construction,
// matching every other constructor in this package.
//
// A denied request answers through errorWriter with the shared JSON
// envelope, coded api.CodeRateLimited, and a Retry-After header set to
// retryAfterSeconds' own whole-second value (see its doc) — recovery (AC 1)
// is inherent to limiter's own token bucket: once it refills, the next
// Allow call passes again, with no separate "unlock" step this middleware
// needs to perform.
func RateLimit(limiter rateLimitAllower, key RateKeyFunc, scope string, recorder rateLimitRecorder, errorWriter rateLimitErrorWriter, now func() time.Time) middleware.Middleware {
	if limiter == nil {
		panic("identity/adapter: RateLimit requires a non-nil rateLimitAllower")
	}
	if key == nil {
		panic("identity/adapter: RateLimit requires a non-nil RateKeyFunc")
	}
	if scope == "" {
		panic("identity/adapter: RateLimit requires a non-empty scope")
	}
	if recorder == nil {
		panic("identity/adapter: RateLimit requires a non-nil rateLimitRecorder")
	}
	if errorWriter == nil {
		panic("identity/adapter: RateLimit requires a non-nil rateLimitErrorWriter")
	}
	if now == nil {
		panic("identity/adapter: RateLimit requires a non-nil now func")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			k := key(r)
			retryAfter, ok := limiter.Allow(k, now())
			if ok {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retryAfter)))
			recorder.RecordLimited(rateLimitMetricPrincipal(k), scope)
			errorWriter(w, http.StatusTooManyRequests, api.CodeRateLimited, rateLimitedMessage)
		})
	}
}

// retryAfterSeconds converts d into the whole-second value the Retry-After
// header carries: ceil'd up (a caller must never be told to retry sooner
// than the bucket will actually have refilled) and floored at 1 (a
// non-positive delay — Allow denying at, or just past, the exact recovery
// instant — must still tell the caller to wait at least a second, never
// "retry immediately"/"retry in 0 seconds").
func retryAfterSeconds(d time.Duration) int {
	seconds := int(math.Ceil(d.Seconds()))
	if seconds < 1 {
		return 1
	}
	return seconds
}
