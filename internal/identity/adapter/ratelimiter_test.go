package adapter

import (
	"fmt"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// TestKeyedRateLimiter_AllowsUpToBurstThenDenies covers AC 1's own shape: a
// client exhausts its burst, the next call is denied with a positive
// retryAfter.
func TestKeyedRateLimiter_AllowsUpToBurstThenDenies(t *testing.T) {
	l := NewKeyedRateLimiter(rate.Limit(1), 3)
	now := time.Now()
	const key = "user:alice"

	for i := range 3 {
		retryAfter, ok := l.Allow(key, now)
		if !ok {
			t.Fatalf("call #%d = denied, want allowed (within burst)", i+1)
		}
		if retryAfter != 0 {
			t.Errorf("call #%d retryAfter = %v, want 0 on an allowed call", i+1, retryAfter)
		}
	}

	retryAfter, ok := l.Allow(key, now)
	if ok {
		t.Fatal("call after burst exhausted = allowed, want denied")
	}
	if retryAfter <= 0 {
		t.Errorf("retryAfter on denial = %v, want a positive duration", retryAfter)
	}
}

// TestKeyedRateLimiter_RecoversAfterRetryAfter covers AC 1's recovery half:
// advancing the injected clock past a denial's own retryAfter allows again.
func TestKeyedRateLimiter_RecoversAfterRetryAfter(t *testing.T) {
	l := NewKeyedRateLimiter(rate.Limit(1), 1)
	now := time.Now()
	const key = "user:alice"

	if _, ok := l.Allow(key, now); !ok {
		t.Fatal("first call = denied, want allowed (burst 1)")
	}
	retryAfter, ok := l.Allow(key, now)
	if ok {
		t.Fatal("second immediate call = allowed, want denied (burst exhausted)")
	}

	later := now.Add(retryAfter)
	if _, ok := l.Allow(key, later); !ok {
		t.Error("call at now+retryAfter = denied, want allowed (bucket must have refilled)")
	}
}

// TestKeyedRateLimiter_DenialReturnsTheReservedToken proves a denied Allow
// call does not permanently consume a token from the bucket — Allow's own
// doc: CancelAt hands the reservation back on denial. Without that, burst-1
// distinct callers would each drain one token even though every one of them
// was rejected, and the bucket would take longer than necessary to recover.
func TestKeyedRateLimiter_DenialReturnsTheReservedToken(t *testing.T) {
	l := NewKeyedRateLimiter(rate.Limit(1), 1)
	now := time.Now()
	const key = "user:alice"

	if _, ok := l.Allow(key, now); !ok {
		t.Fatal("first call = denied, want allowed (burst 1)")
	}

	// Several denied calls back to back must not push the eventual recovery
	// time out any further than the first denial already did.
	var retryAfter time.Duration
	for range 5 {
		var ok bool
		retryAfter, ok = l.Allow(key, now)
		if ok {
			t.Fatal("call while burst exhausted = allowed, want denied")
		}
	}

	later := now.Add(retryAfter)
	if _, ok := l.Allow(key, later); !ok {
		t.Error("call at now+retryAfter after repeated denials = denied, want allowed")
	}
}

// TestKeyedRateLimiter_ZeroBurstFailsClosed covers Allow's own defensive
// branch: RateLimitConfig.Validate never lets a non-positive burst through
// in production, but a KeyedRateLimiter built directly with burst 0 must
// still deny (never panic, and never let the request through unchecked)
// rather than assume ReserveN's own Reservation is always OK.
func TestKeyedRateLimiter_ZeroBurstFailsClosed(t *testing.T) {
	l := NewKeyedRateLimiter(rate.Limit(1), 0)
	if _, ok := l.Allow("user:alice", time.Now()); ok {
		t.Error("Allow with burst 0 = allowed, want denied")
	}
}

// TestKeyedRateLimiter_KeysAreIndependent covers AC 1's "per principal"
// framing: one key's exhausted bucket must not affect another's.
func TestKeyedRateLimiter_KeysAreIndependent(t *testing.T) {
	l := NewKeyedRateLimiter(rate.Limit(1), 1)
	now := time.Now()

	if _, ok := l.Allow("user:alice", now); !ok {
		t.Fatal("alice's first call = denied, want allowed")
	}
	if _, ok := l.Allow("user:alice", now); ok {
		t.Fatal("alice's second immediate call = allowed, want denied")
	}
	if _, ok := l.Allow("user:bob", now); !ok {
		t.Error("bob's first call = denied, want allowed (must be unaffected by alice's exhausted bucket)")
	}
}

// TestKeyedRateLimiter_BoundsMapSize guards the memory-exhaustion vector an
// attacker-supplied key (a client IP, for an anonymous/token-exchange
// caller) opens up. Mirrors LoginAttemptLimiter's own
// TestLoginAttemptLimiter_BoundsMapSize: fill the map to the cap, age every
// entry out, then assert inserting one more distinct key triggers a sweep
// rather than growing the map past the cap.
func TestKeyedRateLimiter_BoundsMapSize(t *testing.T) {
	l := NewKeyedRateLimiter(rate.Limit(10), 10)
	start := time.Now()

	for i := range maxTrackedRateLimiterKeys {
		l.Allow(fmt.Sprintf("ip:10.0.0.%d", i), start)
	}
	if got := len(l.state); got != maxTrackedRateLimiterKeys {
		t.Fatalf("setup: tracked keys = %d, want exactly %d", got, maxTrackedRateLimiterKeys)
	}

	later := start.Add(staleRateLimiterKeyWindow + time.Minute)
	l.Allow("ip:10.0.1.1", later)

	if got := len(l.state); got > maxTrackedRateLimiterKeys {
		t.Errorf("tracked keys after the triggering sweep = %d, want capped at %d", got, maxTrackedRateLimiterKeys)
	}
	if got := len(l.state); got != 1 {
		t.Errorf("tracked keys once every filler has aged out = %d, want 1 (only the newcomer)", got)
	}
}

// TestKeyedRateLimiter_EvictsOnlyStaleEntries asserts the sweep triggered at
// the cap removes only the entry whose last activity has aged out of
// staleRateLimiterKeyWindow, leaving fresher entries — even ones inserted
// well after it — untouched.
func TestKeyedRateLimiter_EvictsOnlyStaleEntries(t *testing.T) {
	l := NewKeyedRateLimiter(rate.Limit(10), 10)
	now := time.Now()
	const recent = "ip:10.0.0.1"
	const stale = "ip:10.0.0.2"

	l.Allow(stale, now)
	stalePast := now.Add(staleRateLimiterKeyWindow + time.Minute)

	for i := range maxTrackedRateLimiterKeys - 1 {
		l.Allow(fmt.Sprintf("ip:10.0.1.%d", i), stalePast)
	}
	l.Allow(recent, stalePast)

	if _, stillTracked := l.state[stale]; stillTracked {
		t.Error("a stale (aged-out) entry survived the sweep")
	}
	if _, stillTracked := l.state[recent]; !stillTracked {
		t.Error("the freshly-inserted entry that triggered the sweep was itself evicted")
	}
	if got := len(l.state); got != maxTrackedRateLimiterKeys {
		t.Errorf("tracked keys after the sweep = %d, want %d (the one stale entry evicted, recent inserted)", got, maxTrackedRateLimiterKeys)
	}
}
