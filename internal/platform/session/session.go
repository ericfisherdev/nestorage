// Package session builds Nestorage's server-side session manager and the
// CSRF helpers layered over it. It is deliberately platform-level (not owned
// by the identity bounded context): NSTR-20's login, NSTR-21's user
// management, and every other feature that needs a session or a CSRF token
// depend on this package rather than on each other.
package session

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"

	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	corecfg "github.com/ericfisherdev/nestcore/config"
	identitysession "github.com/ericfisherdev/nestcore/identity/session"
)

// KeyUserID is the session key under which the authenticated user's id is
// stored. Exported so every consumer (NSTR-19's onboarding wizard, NSTR-20's
// login, NSTR-24's principal resolution) reads and writes the same key
// instead of each minting its own unexported string.
//
// It aliases nestcore's identity/session.KeyMemberID (NSTR-117) rather than
// its own private literal ("user_id" before this change): Nestova must read
// the SAME key for a session shared over identity.sessions to carry a real
// cross-app login, not just a shared cookie/store.
const KeyUserID = identitysession.KeyMemberID

// keyCSRF is the session key backing CSRFToken/VerifyCSRF. Unexported: no
// caller needs the raw session value, only the two functions below.
const keyCSRF = "csrf_token"

// csrfTokenLen is the CSRF token length in bytes (64-char hex string).
const csrfTokenLen = 32

// New constructs an scs.SessionManager over nestcore's shared
// identity/session store (NSTR-117), replacing this package's own pre-
// NSTR-130 identity.sessions shim (see this package's git history for
// identity_store.go, removed by this change): both apps must build their
// manager the same way — literal store, cookie name, and cookie attributes
// — for a session written by one to be readable by the other, not merely an
// equivalent one built independently. stop terminates the store's
// background cleanup goroutine; the composition root (cmd/server/main.go)
// defers it since main returns only at process shutdown.
func New(pool *pgxpool.Pool, cfg corecfg.SessionConfig) (sm *scs.SessionManager, stop func()) {
	return identitysession.NewManager(pool, cfg)
}

// CSRFToken returns the current session's CSRF token, minting a fresh
// 32-byte crypto/rand token on first read and returning the existing one on
// every later call for the same session. A crypto/rand failure returns "",
// which VerifyCSRF then rejects (fails closed).
func CSRFToken(ctx context.Context, sm *scs.SessionManager) string {
	if token := sm.GetString(ctx, keyCSRF); token != "" {
		return token
	}
	b := make([]byte, csrfTokenLen)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	token := hex.EncodeToString(b)
	sm.Put(ctx, keyCSRF, token)
	return token
}

// VerifyCSRF reports whether r carries the current session's CSRF token,
// read from the X-CSRF-Token header first and falling back to the
// csrf_token form field (r.ParseForm must already have run). The comparison
// is constant-time; either side being empty fails closed.
func VerifyCSRF(r *http.Request, sm *scs.SessionManager) bool {
	sessionToken := sm.GetString(r.Context(), keyCSRF)
	if sessionToken == "" {
		return false
	}
	formToken := r.Header.Get("X-CSRF-Token")
	if formToken == "" {
		formToken = r.FormValue("csrf_token")
	}
	if formToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(sessionToken), []byte(formToken)) == 1
}
