package adapter

import (
	"sync"
	"time"
)

// Login attempt-limiting tuning: five consecutive wrong passwords lock the
// account out for fifteen minutes, mirroring the design Nestova proved out
// for its own login MFA step (internal/auth/adapter/login_attempt_limiter.go).
const (
	// loginAttemptThreshold is how many consecutive wrong passwords an
	// email may submit before the lockout engages.
	loginAttemptThreshold = 5
	// loginLockoutWindow is how long an email is locked out of login after
	// the (threshold+1)th consecutive wrong password.
	loginLockoutWindow = 15 * time.Minute
	// maxTrackedAccounts bounds the limiter's map. Unlike Nestova's MFA
	// limiter — keyed by an already-authenticated member id — this one is
	// keyed by attacker-supplied email, so an unbounded map is a memory
	// exhaustion vector. When the cap is reached, recordFailure sweeps
	// entries whose last activity has aged out of the lockout window before
	// inserting a new one.
	maxTrackedAccounts = 10_000
)

// loginAttemptState is one email's in-memory login strike state.
type loginAttemptState struct {
	failures     int
	lockedUntil  time.Time
	lastActivity time.Time
}

// LoginAttemptLimiter tracks consecutive wrong passwords per (lowercased)
// email and enforces a lockout window after loginAttemptThreshold
// consecutive failures.
//
// Exported, and constructed exactly once by the composition root (see
// cmd/server/main.go), so the same instance can be injected into both
// Handlers (the session-cookie /login endpoint) and NSTR-22's
// DeviceTokenService (the POST /api/v1/auth/device-tokens exchange): both
// verify a password against the same credential store, so an attacker
// locked out of one must not get a fresh run of attempts against the other.
//
// State is in-memory and process-lifetime — matching Nestova's accepted
// tradeoff for this deployment shape: a single-household, local-first
// appliance restart clearing lockouts is no worse than the attacker simply
// waiting out the window. The key here is attacker-supplied, though, so the
// map is bounded (see maxTrackedAccounts) unlike Nestova's member-keyed one.
//
// Once locked, an email stays locked until lockedUntil regardless of
// further attempts — every caller checks Locked() BEFORE touching the
// Authenticator at all and never reaches RecordFailure again until the
// window has passed. Once it HAS passed, RecordFailure resets the strike
// count before counting the new failure, so an email that waited out a
// lockout gets a full fresh run of loginAttemptThreshold attempts rather
// than the counter continuing to climb from where the expired lockout left
// it.
type LoginAttemptLimiter struct {
	mu    sync.Mutex
	state map[string]*loginAttemptState
}

// NewLoginAttemptLimiter constructs an empty limiter.
func NewLoginAttemptLimiter() *LoginAttemptLimiter {
	return &LoginAttemptLimiter{state: make(map[string]*loginAttemptState)}
}

// Locked reports whether email is currently in a lockout window as of now.
func (l *LoginAttemptLimiter) Locked(email string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.state[email]
	if !ok {
		return false
	}
	return now.Before(st.lockedUntil)
}

// RecordFailure records a wrong password for email as of now, returning
// lockedOut=true exactly once — on the attempt that CROSSES the threshold —
// so the caller logs exactly one lockout line per lockout rather than one
// per subsequent attempt.
//
// If email's PRIOR lockout has already expired as of now, the strike count
// is reset to zero before counting this failure: without this, an email
// that waits out a lockout and then enters one more wrong password would
// never be locked out again (failures would climb past
// loginAttemptThreshold+1 without ever landing on it exactly).
func (l *LoginAttemptLimiter) RecordFailure(email string, now time.Time) (lockedOut bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.state[email]
	switch {
	case !ok:
		if len(l.state) >= maxTrackedAccounts {
			l.evictStaleLocked(now)
		}
		st = &loginAttemptState{}
		l.state[email] = st
	case !st.lockedUntil.IsZero() && !now.Before(st.lockedUntil):
		// A previous lockout has fully expired: start counting fresh.
		st.failures = 0
		st.lockedUntil = time.Time{}
	}
	st.failures++
	st.lastActivity = now
	if st.failures == loginAttemptThreshold+1 {
		st.lockedUntil = now.Add(loginLockoutWindow)
		return true
	}
	return false
}

// RecordSuccess clears email's strike state after a successful login.
func (l *LoginAttemptLimiter) RecordSuccess(email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.state, email)
}

// evictStaleLocked sweeps entries whose last activity predates the lockout
// window, bounding the map even under an attacker spraying many distinct
// emails. Callers must hold l.mu.
func (l *LoginAttemptLimiter) evictStaleLocked(now time.Time) {
	cutoff := now.Add(-loginLockoutWindow)
	for email, st := range l.state {
		if st.lastActivity.Before(cutoff) {
			delete(l.state, email)
		}
	}
}
