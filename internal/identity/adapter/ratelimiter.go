package adapter

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// maxTrackedRateLimiterKeys bounds a KeyedRateLimiter's own map. Like
// LoginAttemptLimiter's maxTrackedAccounts, a key here can be
// attacker-influenced (a client IP for an anonymous or token-exchange
// request — see PrincipalRateKey/ClientIPRateKey in ratelimit.go), so an
// unbounded map is a memory-exhaustion vector. When the cap is reached,
// limiterFor sweeps entries whose last activity has aged out of
// staleRateLimiterKeyWindow before inserting a new one, mirroring
// loginattemptlimiter.go's own maxTrackedAccounts/evictStaleLocked shape.
const maxTrackedRateLimiterKeys = 10_000

// staleRateLimiterKeyWindow is how long a key's token-bucket state survives
// with no Allow call before it becomes eligible for eviction under the cap
// above. Unlike LoginAttemptLimiter's lockout window (a real domain
// concept both the state machine and the sweep share), a token bucket has
// no comparable fixed horizon — this is simply "long enough that any client
// mid-burst never loses its bucket state, short enough that a sprayed cap
// recovers promptly": ten minutes comfortably outlasts every RPS/RPM window
// RateLimitConfig can be set to.
const staleRateLimiterKeyWindow = 10 * time.Minute

// keyedLimiterEntry is one key's in-memory token-bucket state plus when it
// was last touched — the bookkeeping evictStaleLocked sweeps on.
type keyedLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// KeyedRateLimiter is a bounded set of independent token buckets, one per
// key, sharing one limit/burst set at construction — the in-process,
// in-memory rate limiter both of NSTR-58's buckets (the account-wide
// /api/v1 bucket and the stricter token-exchange bucket) are built from.
// See RateLimit (ratelimit.go), its only consumer.
type KeyedRateLimiter struct {
	mu    sync.Mutex
	limit rate.Limit
	burst int
	state map[string]*keyedLimiterEntry
}

// NewKeyedRateLimiter constructs an empty KeyedRateLimiter: every key's own
// bucket refills at limit and holds at most burst tokens.
func NewKeyedRateLimiter(limit rate.Limit, burst int) *KeyedRateLimiter {
	return &KeyedRateLimiter{limit: limit, burst: burst, state: make(map[string]*keyedLimiterEntry)}
}

// Allow reports whether key may act now: ok=true (retryAfter=0) when a
// token was available, ok=false with retryAfter set to how long key must
// wait before its next token when none was. The request is REJECTED, not
// queued — on a denial, the reservation's token is handed back via
// CancelAt so a caller that never actually proceeds does not permanently
// lose it from the bucket.
func (l *KeyedRateLimiter) Allow(key string, now time.Time) (retryAfter time.Duration, ok bool) {
	lim := l.limiterFor(key, now)

	res := lim.ReserveN(now, 1)
	if !res.OK() {
		// Only reachable when burst < 1, which RateLimitConfig.Validate
		// never allows through in production — fail closed rather than
		// panic or let the request through unchecked.
		return 0, false
	}
	if delay := res.DelayFrom(now); delay > 0 {
		res.CancelAt(now)
		return delay, false
	}
	return 0, true
}

// limiterFor returns key's own *rate.Limiter, constructing one — and
// recording this call as its most recent activity — on first use. The
// returned limiter is read out from under l.mu before ReserveN runs on it:
// rate.Limiter has its own internal locking and is safe for concurrent use,
// so holding l.mu across that call would only add unnecessary contention
// between unrelated keys.
func (l *KeyedRateLimiter) limiterFor(key string, now time.Time) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	if e, exists := l.state[key]; exists {
		e.lastSeen = now
		return e.limiter
	}
	if len(l.state) >= maxTrackedRateLimiterKeys {
		l.evictStaleLocked(now)
	}
	e := &keyedLimiterEntry{limiter: rate.NewLimiter(l.limit, l.burst), lastSeen: now}
	l.state[key] = e
	return e.limiter
}

// evictStaleLocked sweeps entries whose last activity predates
// staleRateLimiterKeyWindow, bounding the map even under an attacker
// spraying many distinct keys. Callers must hold l.mu.
func (l *KeyedRateLimiter) evictStaleLocked(now time.Time) {
	cutoff := now.Add(-staleRateLimiterKeyWindow)
	for key, e := range l.state {
		if e.lastSeen.Before(cutoff) {
			delete(l.state, key)
		}
	}
}
