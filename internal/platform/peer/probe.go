// Package peer implements NSTR-124's server-side reachability probe against
// a configured sibling app (Nestova's PEER_NESTOVA_URL). Nothing here is
// ever called from the browser: the shell probes the peer's /healthz on the
// server and renders the cached verdict, so the browser itself never issues
// a cross-origin request and no external-host rule is ever at risk.
package peer

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// DefaultProbeTimeout bounds a single /healthz request so sidebar rendering
// never blocks on a dead or slow peer — NSTR-124's own AC that "the page
// still renders at full speed" even with the peer down.
const DefaultProbeTimeout = 300 * time.Millisecond

// DefaultVerdictTTL caches a probe's outcome so a request landing within TTL
// of the last check never re-probes the peer. The bound is per cache
// window, not per instant: Reachable releases the mutex before probing, so
// renders that miss the cache concurrently (e.g. right after a cold start
// or right after the previous TTL window expired) each issue their own
// /healthz request until the first verdict lands.
const DefaultVerdictTTL = 30 * time.Second

// Prober is a cached, timeout-bounded reachability check against a peer
// app's /healthz. It satisfies the one-method reachability port shell.go
// depends on (see cmd/server/shell.go's shellPeerReachabilityChecker doc).
// The zero value is not usable — construct with NewProber.
type Prober struct {
	client  *http.Client
	baseURL string
	timeout time.Duration
	ttl     time.Duration

	mu        sync.Mutex
	checkedAt time.Time
	verdict   bool
}

// NewProber constructs a Prober against baseURL's /healthz, injecting client
// and the timeout/ttl so tests can point it at an httptest.Server and a
// fast-expiring ttl instead of a real network call and DefaultVerdictTTL's
// real-time wait. baseURL may be empty (no peer configured): Reachable is
// simply never called in that case — see shellDataService.Peer's own doc —
// so an empty baseURL is not itself rejected here.
func NewProber(client *http.Client, baseURL string, timeout, ttl time.Duration) *Prober {
	if client == nil {
		panic("peer: NewProber requires a non-nil *http.Client")
	}
	return &Prober{client: client, baseURL: baseURL, timeout: timeout, ttl: ttl}
}

// Reachable reports whether baseURL's /healthz answered 200 within timeout,
// caching the verdict for ttl so repeated sidebar renders within the same
// window never re-probe a peer that was just checked. ctx contributes
// values only (e.g. a logger or trace ID) — it does NOT bound or cancel
// the probe itself, only p.timeout does (see probe's own doc for why: the
// verdict is process-wide cached state, so one caller's context being
// canceled must never poison every other render's cache for the rest of
// ttl).
func (p *Prober) Reachable(ctx context.Context) bool {
	if v, ok := p.cached(); ok {
		return v
	}

	ok := p.probe(ctx)

	p.mu.Lock()
	p.verdict = ok
	p.checkedAt = time.Now()
	p.mu.Unlock()

	return ok
}

// cached returns the last verdict and true when it is still within ttl, or
// (false, false) when a fresh probe is needed.
func (p *Prober) cached() (bool, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.checkedAt.IsZero() || time.Since(p.checkedAt) >= p.ttl {
		return false, false
	}
	return p.verdict, true
}

// probe issues the actual GET {baseURL}/healthz, bounded by timeout.
//
// context.WithoutCancel detaches ctx's own cancellation (keeping only its
// values) before WithTimeout applies p.timeout: the verdict this produces
// is process-wide cached state, not per-request data, so a caller's
// context being canceled mid-probe (e.g. a browser navigating away or a
// kiosk tab closing) must not be recorded as "unreachable" and then served
// to every OTHER render for the rest of ttl. p.timeout alone bounds the
// request.
func (p *Prober) probe(ctx context.Context) bool {
	reqCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, p.baseURL+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode == http.StatusOK
}
