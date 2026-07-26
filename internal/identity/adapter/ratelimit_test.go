package adapter_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ericfisherdev/nestcore/httpserver/middleware"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/api"
)

// fakeAllower is a rateLimitAllower test double: it returns a fixed
// (retryAfter, ok) pair regardless of key/now, so RateLimit's own
// request-handling logic (headers, body, recorder, next-vs-not) can be
// tested independent of KeyedRateLimiter's real token-bucket timing
// (covered by ratelimiter_test.go instead).
type fakeAllower struct {
	retryAfter time.Duration
	ok         bool
}

func (f *fakeAllower) Allow(string, time.Time) (time.Duration, bool) {
	return f.retryAfter, f.ok
}

// spyRecorder is a rateLimitRecorder test double recording every
// (principal, scope) pair RecordLimited was called with.
type spyRecorder struct {
	calls []spyRecorderCall
}

type spyRecorderCall struct{ principal, scope string }

func (s *spyRecorder) RecordLimited(principal, scope string) {
	s.calls = append(s.calls, spyRecorderCall{principal, scope})
}

// capturingErrorWriter is a rateLimitErrorWriter test double recording its
// own single call, so a test can assert RateLimit invoked it with the
// expected status/code/message without depending on api.WriteError's real
// JSON encoding (that shape is platform/api's own test responsibility).
type capturingErrorWriter struct {
	calls   int
	status  int
	code    api.Code
	message string
}

func (c *capturingErrorWriter) write(w http.ResponseWriter, status int, code api.Code, message string) {
	c.calls++
	c.status = status
	c.code = code
	c.message = message
	w.WriteHeader(status)
}

func fixedNow() time.Time { return time.Time{} }

func rateLimitCalledFlag() (http.Handler, *bool) {
	called := false
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}), &called
}

func TestRateLimit_NilDependenciesPanic(t *testing.T) {
	limiter := &fakeAllower{ok: true}
	recorder := &spyRecorder{}
	errWriter := (&capturingErrorWriter{}).write

	cases := []struct {
		name string
		fn   func()
	}{
		{"nil limiter", func() { adapter.RateLimit(nil, adapter.ClientIPRateKey, "api", recorder, errWriter, fixedNow) }},
		{"nil key", func() { adapter.RateLimit(limiter, nil, "api", recorder, errWriter, fixedNow) }},
		{"empty scope", func() { adapter.RateLimit(limiter, adapter.ClientIPRateKey, "", recorder, errWriter, fixedNow) }},
		{"nil recorder", func() { adapter.RateLimit(limiter, adapter.ClientIPRateKey, "api", nil, errWriter, fixedNow) }},
		{"nil errorWriter", func() { adapter.RateLimit(limiter, adapter.ClientIPRateKey, "api", recorder, nil, fixedNow) }},
		{"nil now", func() { adapter.RateLimit(limiter, adapter.ClientIPRateKey, "api", recorder, errWriter, nil) }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("RateLimit(%s) did not panic", tt.name)
				}
			}()
			tt.fn()
		})
	}
}

func TestRateLimit_Allowed_PassesThroughUntouched(t *testing.T) {
	limiter := &fakeAllower{ok: true}
	recorder := &spyRecorder{}
	errWriter := (&capturingErrorWriter{}).write
	next, called := rateLimitCalledFlag()

	handler := adapter.RateLimit(limiter, adapter.ClientIPRateKey, "api", recorder, errWriter, fixedNow)(next)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/bins", nil))

	if !*called {
		t.Error("RateLimit must call next when the limiter allows")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Errorf("Retry-After = %q, want unset on an allowed request", got)
	}
	if len(recorder.calls) != 0 {
		t.Errorf("recorder calls = %d, want 0 on an allowed request", len(recorder.calls))
	}
}

func TestRateLimit_Denied_AnswersRateLimitedAndNeverCallsNext(t *testing.T) {
	limiter := &fakeAllower{ok: false, retryAfter: 2500 * time.Millisecond}
	recorder := &spyRecorder{}
	writer := &capturingErrorWriter{}
	next, called := rateLimitCalledFlag()

	handler := adapter.RateLimit(limiter, adapter.ClientIPRateKey, "auth", recorder, writer.write, fixedNow)(next)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/device-tokens", nil))

	if *called {
		t.Error("RateLimit must not call next when the limiter denies")
	}
	if writer.calls != 1 {
		t.Fatalf("errorWriter calls = %d, want 1", writer.calls)
	}
	if writer.status != http.StatusTooManyRequests {
		t.Errorf("errorWriter status = %d, want %d", writer.status, http.StatusTooManyRequests)
	}
	if writer.code != api.CodeRateLimited {
		t.Errorf("errorWriter code = %q, want %q", writer.code, api.CodeRateLimited)
	}
	if writer.message == "" {
		t.Error("errorWriter message = \"\", want a non-empty human-readable message")
	}
	// 2500ms ceils to 3 whole seconds — Retry-After must never under-report
	// how long the caller actually has to wait.
	if got := rec.Header().Get("Retry-After"); got != "3" {
		t.Errorf("Retry-After = %q, want %q (2500ms ceil'd)", got, "3")
	}
}

// TestRateLimit_Denied_RetryAfterFlooredAtOneSecond covers a non-positive
// retryAfter (Allow denying at, or just past, the exact recovery instant):
// Retry-After must still read 1, never 0 or a negative value — "retry
// immediately" is not a valid instruction for a 429.
func TestRateLimit_Denied_RetryAfterFlooredAtOneSecond(t *testing.T) {
	limiter := &fakeAllower{ok: false, retryAfter: 0}
	recorder := &spyRecorder{}
	writer := &capturingErrorWriter{}
	next, _ := rateLimitCalledFlag()

	handler := adapter.RateLimit(limiter, adapter.ClientIPRateKey, "api", recorder, writer.write, fixedNow)(next)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/bins", nil))

	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want %q (floored at 1)", got, "1")
	}
}

func TestRateLimit_Denied_RecordsPrincipalAndScope(t *testing.T) {
	limiter := &fakeAllower{ok: false, retryAfter: time.Second}
	recorder := &spyRecorder{}
	writer := &capturingErrorWriter{}
	next, _ := rateLimitCalledFlag()

	handler := adapter.RateLimit(limiter, adapter.ClientIPRateKey, "auth", recorder, writer.write, fixedNow)(next)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/device-tokens", nil))

	if len(recorder.calls) != 1 {
		t.Fatalf("recorder calls = %d, want 1", len(recorder.calls))
	}
	if recorder.calls[0].scope != "auth" {
		t.Errorf("recorded scope = %q, want %q", recorder.calls[0].scope, "auth")
	}
	// ClientIPRateKey always produces an "ip:"-prefixed key, which
	// RateLimit's own metric-label collapse must report as "anonymous",
	// never the raw (attacker-controlled) address — see
	// rateLimitMetricPrincipal's own doc.
	if recorder.calls[0].principal != "anonymous" {
		t.Errorf("recorded principal = %q, want %q", recorder.calls[0].principal, "anonymous")
	}
}

// TestRateLimit_DeniedPrincipal_RecordsRawKeyNotCollapsed covers the OTHER
// half of rateLimitMetricPrincipal's collapse rule: a "user:<id>" key must
// be recorded verbatim (bounded by household size), not collapsed to
// "anonymous".
func TestRateLimit_DeniedPrincipal_RecordsRawKeyNotCollapsed(t *testing.T) {
	limiter := &fakeAllower{ok: false, retryAfter: time.Second}
	recorder := &spyRecorder{}
	writer := &capturingErrorWriter{}
	next, _ := rateLimitCalledFlag()

	fixedKey := func(*http.Request) string { return "user:11111111-1111-1111-1111-111111111111" }
	handler := adapter.RateLimit(limiter, fixedKey, "api", recorder, writer.write, fixedNow)(next)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/bins", nil))

	if len(recorder.calls) != 1 {
		t.Fatalf("recorder calls = %d, want 1", len(recorder.calls))
	}
	if recorder.calls[0].principal != "user:11111111-1111-1111-1111-111111111111" {
		t.Errorf("recorded principal = %q, want the verbatim key", recorder.calls[0].principal)
	}
}

// TestRateLimit_OneLimitedPrincipalNeverBlocksAnother uses the REAL
// KeyedRateLimiter (not fakeAllower) to prove two distinct keys' buckets
// are genuinely independent through the full middleware, not just at
// KeyedRateLimiter's own unit-test level (ratelimiter_test.go).
func TestRateLimit_OneLimitedPrincipalNeverBlocksAnother(t *testing.T) {
	limiter := adapter.NewKeyedRateLimiter(1, 1)
	recorder := &spyRecorder{}
	writer := &capturingErrorWriter{}

	keyFor := func(principal string) adapter.RateKeyFunc {
		return func(*http.Request) string { return principal }
	}

	next, _ := rateLimitCalledFlag()
	aliceHandler := adapter.RateLimit(limiter, keyFor("user:alice"), "api", recorder, writer.write, fixedNow)(next)
	bobHandler := adapter.RateLimit(limiter, keyFor("user:bob"), "api", recorder, writer.write, fixedNow)(next)

	// Exhaust alice's own bucket.
	aliceHandler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/bins", nil))
	rec := httptest.NewRecorder()
	aliceHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/bins", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("setup: alice's second call = %d, want %d (burst exhausted)", rec.Code, http.StatusTooManyRequests)
	}

	bobRec := httptest.NewRecorder()
	bobHandler.ServeHTTP(bobRec, httptest.NewRequest(http.MethodGet, "/api/v1/bins", nil))
	if bobRec.Code != http.StatusOK {
		t.Errorf("bob's call after alice was limited = %d, want %d (independent buckets)", bobRec.Code, http.StatusOK)
	}
}

// resolvedPrincipalRequest drives r through a real adapter.Resolve chain
// that unconditionally resolves to principal, mirroring
// TestResolve_ValidCredential_StoresPrincipalForNextToRead's own approach
// (middleware_test.go) — the only way CurrentPrincipal ends up populated in
// r's context outside of production's own global Resolve middleware.
func resolvedPrincipalRequest(t *testing.T, principal domain.Principal, r *http.Request) *http.Request {
	t.Helper()
	chain := adapter.NewChain(&stubResolver{principal: principal, found: true}, &stubResolver{}, &stubResolver{})
	denier := adapter.NewDenier(testLogger())

	var got *http.Request
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r })
	adapter.Resolve(chain, denier, testLogger())(next).ServeHTTP(httptest.NewRecorder(), r)
	return got
}

func TestPrincipalRateKey_UserPrincipal(t *testing.T) {
	id := domain.NewUserID()
	principal := domain.NewUserPrincipal(id, domain.RoleMember, "Daniel")
	r := resolvedPrincipalRequest(t, principal, httptest.NewRequest(http.MethodGet, "/api/v1/bins", nil))

	want := "user:" + id.String()
	if got := adapter.PrincipalRateKey(r); got != want {
		t.Errorf("PrincipalRateKey = %q, want %q", got, want)
	}
}

func TestPrincipalRateKey_IntegrationPrincipal(t *testing.T) {
	principal := domain.NewIntegrationPrincipal("Nestova")
	r := resolvedPrincipalRequest(t, principal, httptest.NewRequest(http.MethodGet, "/api/v1/bins", nil))

	if got := adapter.PrincipalRateKey(r); got != "integration" {
		t.Errorf("PrincipalRateKey = %q, want %q", got, "integration")
	}
}

func TestPrincipalRateKey_Anonymous_KeysByClientIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/bins", nil)
	r.RemoteAddr = "192.0.2.1:1234"

	var got string
	captured := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = adapter.PrincipalRateKey(r)
	})
	middleware.ForwardedHeaders(nil)(captured).ServeHTTP(httptest.NewRecorder(), r)

	if got != "ip:192.0.2.1" {
		t.Errorf("PrincipalRateKey (anonymous) = %q, want %q", got, "ip:192.0.2.1")
	}
}

func TestClientIPRateKey_AlwaysKeysByClientIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/device-tokens", nil)
	r.RemoteAddr = "203.0.113.9:5555"

	// Even with a resolved principal in context (shouldn't happen for the
	// unauthenticated exchange route in production, but ClientIPRateKey
	// must ignore it regardless), the key stays IP-based.
	principal := domain.NewUserPrincipal(domain.NewUserID(), domain.RoleMember, "Daniel")
	r = resolvedPrincipalRequest(t, principal, r)

	var got string
	captured := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = adapter.ClientIPRateKey(r)
	})
	middleware.ForwardedHeaders(nil)(captured).ServeHTTP(httptest.NewRecorder(), r)

	if got != "ip:203.0.113.9" {
		t.Errorf("ClientIPRateKey = %q, want %q", got, "ip:203.0.113.9")
	}
}
