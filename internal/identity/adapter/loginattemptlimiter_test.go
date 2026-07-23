package adapter

import (
	"fmt"
	"testing"
	"time"
)

func TestLoginAttemptLimiter_AllowsUpToThreshold(t *testing.T) {
	l := newLoginAttemptLimiter()
	now := time.Now()
	const email = "alice@example.com"

	for i := range loginAttemptThreshold {
		if l.locked(email, now) {
			t.Fatalf("locked after %d failures, want not locked until the (threshold+1)th", i)
		}
		if l.recordFailure(email, now) {
			t.Fatalf("recordFailure #%d reported lockedOut=true, want false (threshold not yet crossed)", i+1)
		}
	}
	if l.locked(email, now) {
		t.Error("locked after exactly threshold failures, want still not locked")
	}
}

func TestLoginAttemptLimiter_LocksOnThresholdPlusOne(t *testing.T) {
	l := newLoginAttemptLimiter()
	now := time.Now()
	const email = "alice@example.com"

	for range loginAttemptThreshold {
		l.recordFailure(email, now)
	}
	lockedOut := l.recordFailure(email, now)
	if !lockedOut {
		t.Fatal("recordFailure on the (threshold+1)th attempt reported lockedOut=false, want true")
	}
	if !l.locked(email, now) {
		t.Error("email must be locked immediately after crossing the threshold")
	}
}

func TestLoginAttemptLimiter_ReportsLockedOutExactlyOnce(t *testing.T) {
	l := newLoginAttemptLimiter()
	now := time.Now()
	const email = "alice@example.com"

	for range loginAttemptThreshold {
		l.recordFailure(email, now)
	}
	if !l.recordFailure(email, now) {
		t.Fatal("the crossing attempt must report lockedOut=true")
	}
	if l.recordFailure(email, now) {
		t.Error("a SUBSEQUENT failure while already locked must not report lockedOut=true again")
	}
}

func TestLoginAttemptLimiter_UnlocksAfterWindow(t *testing.T) {
	l := newLoginAttemptLimiter()
	now := time.Now()
	const email = "alice@example.com"

	for i := 0; i <= loginAttemptThreshold; i++ {
		l.recordFailure(email, now)
	}
	if !l.locked(email, now) {
		t.Fatal("email must be locked immediately after crossing the threshold")
	}
	after := now.Add(loginLockoutWindow + time.Second)
	if l.locked(email, after) {
		t.Error("email must not be locked once the lockout window has elapsed")
	}
}

func TestLoginAttemptLimiter_RecordSuccessClearsState(t *testing.T) {
	l := newLoginAttemptLimiter()
	now := time.Now()
	const email = "alice@example.com"

	for range loginAttemptThreshold - 1 {
		l.recordFailure(email, now)
	}
	l.recordSuccess(email)

	// After a reset, it must take a FULL fresh run of threshold+1 failures
	// to lock out again — the prior near-threshold count must not carry
	// over.
	for i := range loginAttemptThreshold {
		if l.recordFailure(email, now) {
			t.Fatalf("recordFailure #%d after a reset reported lockedOut=true too early", i+1)
		}
	}
}

// lockUntilThresholdCrossed drives email into its first lockout at now via
// threshold+1 failures, failing the test if that lockout did not take.
func lockUntilThresholdCrossed(t *testing.T, l *loginAttemptLimiter, email string, now time.Time) {
	t.Helper()
	for i := 0; i <= loginAttemptThreshold; i++ {
		l.recordFailure(email, now)
	}
	if !l.locked(email, now) {
		t.Fatal("setup: expected email to be locked after crossing the threshold")
	}
}

// recordFreshFailures records n failures for email at ts, returning the
// LAST call's lockedOut result.
func recordFreshFailures(l *loginAttemptLimiter, email string, ts time.Time, n int) (lastLockedOut bool) {
	for range n {
		lastLockedOut = l.recordFailure(email, ts)
	}
	return lastLockedOut
}

// TestLoginAttemptLimiter_ExpiredLockoutResetsStrikeCount covers a strike
// count that must reset on an expired lockout: an email that waits out a
// lockout and then enters a few more wrong passwords must get a FULL fresh
// run of loginAttemptThreshold attempts before locking out again — not a
// lockout on the very next wrong password, and not permanently unlockable
// because the counter had already climbed past the exact threshold+1 value
// the check looks for.
func TestLoginAttemptLimiter_ExpiredLockoutResetsStrikeCount(t *testing.T) {
	tests := []struct {
		name            string
		freshFailures   int // failures recorded AFTER the first lockout expires
		wantFinalLocked bool
	}{
		{name: "a single fresh failure after expiry does not relock", freshFailures: 1, wantFinalLocked: false},
		{name: "exactly threshold fresh failures after expiry do not relock", freshFailures: loginAttemptThreshold, wantFinalLocked: false},
		{name: "threshold+1 fresh failures after expiry relocks", freshFailures: loginAttemptThreshold + 1, wantFinalLocked: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := newLoginAttemptLimiter()
			now := time.Now()
			const email = "alice@example.com"

			lockUntilThresholdCrossed(t, l, email, now)

			// Wait out the lockout window entirely.
			afterWindow := now.Add(loginLockoutWindow + time.Second)
			if l.locked(email, afterWindow) {
				t.Fatal("setup: expected email to be unlocked once the lockout window has elapsed")
			}

			// Record tt.freshFailures wrong passwords post-expiry; the LAST
			// call's lockedOut result and the final locked() state must
			// reflect a FRESH count, not one carried over from before expiry.
			lastLockedOut := recordFreshFailures(l, email, afterWindow, tt.freshFailures)
			if got := l.locked(email, afterWindow); got != tt.wantFinalLocked {
				t.Errorf("locked() after %d fresh failures post-expiry = %v, want %v (strike count did not reset on expiry)", tt.freshFailures, got, tt.wantFinalLocked)
			}
			if tt.wantFinalLocked && !lastLockedOut {
				t.Error("expected the fresh failure that crossed the threshold to report lockedOut=true")
			}
		})
	}
}

func TestLoginAttemptLimiter_AccountsAreIndependent(t *testing.T) {
	l := newLoginAttemptLimiter()
	now := time.Now()

	for i := 0; i <= loginAttemptThreshold; i++ {
		l.recordFailure("alice@example.com", now)
	}
	if !l.locked("alice@example.com", now) {
		t.Fatal("alice@example.com must be locked")
	}
	if l.locked("bob@example.com", now) {
		t.Error("bob@example.com must be unaffected by alice's lockout")
	}
}

// TestLoginAttemptLimiter_BoundsMapSize guards the memory-exhaustion vector
// an attacker-supplied key opens up. The sweep is best-effort — it can only
// evict entries that are ACTUALLY stale (older than the lockout window),
// since evicting a still-active lockout would defeat the protection — so
// this fills the map to the cap, ages every entry out, then asserts that
// inserting one more distinct email triggers a sweep rather than growing
// the map past the cap.
func TestLoginAttemptLimiter_BoundsMapSize(t *testing.T) {
	l := newLoginAttemptLimiter()
	start := time.Now()

	for i := range maxTrackedAccounts {
		l.recordFailure(fmt.Sprintf("user-%d@example.com", i), start)
	}
	if got := len(l.state); got != maxTrackedAccounts {
		t.Fatalf("setup: tracked accounts = %d, want exactly %d", got, maxTrackedAccounts)
	}

	// Once every filled entry has aged out of the lockout window, one more
	// distinct email must trigger a sweep rather than growing the map past
	// the cap.
	later := start.Add(loginLockoutWindow + time.Minute)
	l.recordFailure("newcomer@example.com", later)

	if len(l.state) > maxTrackedAccounts {
		t.Errorf("tracked accounts after the triggering sweep = %d, want capped at %d", len(l.state), maxTrackedAccounts)
	}
	if got := len(l.state); got != 1 {
		t.Errorf("tracked accounts once every filler has aged out = %d, want 1 (only the newcomer)", got)
	}
}

// TestLoginAttemptLimiter_EvictsOnlyStaleEntries asserts the sweep triggered
// at the cap removes only the entry whose last activity has aged out of the
// lockout window, leaving fresher entries — even ones inserted well after
// it — untouched.
func TestLoginAttemptLimiter_EvictsOnlyStaleEntries(t *testing.T) {
	l := newLoginAttemptLimiter()
	now := time.Now()
	const recent = "recent@example.com"

	// One email locked out at `now`; by stalePast its activity has aged
	// out of the lockout window, making it eligible for eviction.
	for i := 0; i <= loginAttemptThreshold; i++ {
		l.recordFailure("stale@example.com", now)
	}
	stalePast := now.Add(loginLockoutWindow + time.Minute)

	// Fill the map to just under the cap with entries fresh as of
	// stalePast, so the next insert (recent) is the one that crosses the
	// cap and triggers the sweep.
	for i := range maxTrackedAccounts - 1 {
		l.recordFailure(fmt.Sprintf("filler-%d@example.com", i), stalePast)
	}
	l.recordFailure(recent, stalePast)

	if _, stillTracked := l.state["stale@example.com"]; stillTracked {
		t.Error("a stale (aged-out) entry survived the sweep")
	}
	if _, stillTracked := l.state[recent]; !stillTracked {
		t.Error("the freshly-inserted entry that triggered the sweep was itself evicted")
	}
	if got := len(l.state); got != maxTrackedAccounts {
		t.Errorf("tracked accounts after the sweep = %d, want %d (the one stale entry evicted, recent inserted)", got, maxTrackedAccounts)
	}
}
