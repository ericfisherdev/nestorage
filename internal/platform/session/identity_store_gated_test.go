package session_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"

	corecfg "github.com/ericfisherdev/nestcore/config"

	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
)

// newGatedSessionManager returns a session.SessionManager over this
// package's own derived database (dbtest.Harness.NewIsolatedPool must be
// called exactly once per test), exercising the real identity.sessions
// store (identity_store.go) rather than an in-memory fake.
func newGatedSessionManager(t *testing.T) *scs.SessionManager {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "session")
	return session.New(pool, corecfg.SessionConfig{Lifetime: time.Hour}, slog.New(slog.DiscardHandler))
}

// sessionRoundTripHarness drives a real HTTP round trip with a cookie jar
// over sm, the same pattern session_test.go's own csrfHarness uses, so the
// session cookie set by one request is the one read back on the next.
type sessionRoundTripHarness struct {
	server *httptest.Server
	client *http.Client
}

func newSessionRoundTripHarness(t *testing.T, sm *scs.SessionManager, mux *http.ServeMux) *sessionRoundTripHarness {
	t.Helper()
	server := httptest.NewServer(sm.LoadAndSave(mux))
	t.Cleanup(server.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	return &sessionRoundTripHarness{server: server, client: &http.Client{Jar: jar}}
}

func (h *sessionRoundTripHarness) get(t *testing.T, path string) {
	t.Helper()
	resp, err := h.client.Get(h.server.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	_ = resp.Body.Close()
}

// TestNew_CommitsAndFindsAgainstRealDatabase proves identityStore's
// Commit/Find round trip through a real identity.sessions row: a value put
// in one request is read back in a later request sharing the same session
// cookie.
func TestNew_CommitsAndFindsAgainstRealDatabase(t *testing.T) {
	sm := newGatedSessionManager(t)

	var readBack string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /write", func(_ http.ResponseWriter, r *http.Request) {
		sm.Put(r.Context(), "greeting", "hello")
	})
	mux.HandleFunc("GET /read", func(_ http.ResponseWriter, r *http.Request) {
		readBack = sm.GetString(r.Context(), "greeting")
	})

	h := newSessionRoundTripHarness(t, sm, mux)
	h.get(t, "/write")
	h.get(t, "/read")

	if readBack != "hello" {
		t.Errorf("GetString after a round trip = %q, want %q", readBack, "hello")
	}
}

// TestNew_DestroyDeletesTheRow proves identityStore.Delete: destroying a
// session, then reloading the same cookie, must not resurrect any of its
// data — the row is really gone, not just marked.
func TestNew_DestroyDeletesTheRow(t *testing.T) {
	sm := newGatedSessionManager(t)

	var readBack string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /write", func(_ http.ResponseWriter, r *http.Request) {
		sm.Put(r.Context(), "greeting", "hello")
	})
	mux.HandleFunc("GET /destroy", func(_ http.ResponseWriter, r *http.Request) {
		if err := sm.Destroy(r.Context()); err != nil {
			t.Errorf("Destroy: %v", err)
		}
	})
	mux.HandleFunc("GET /read", func(_ http.ResponseWriter, r *http.Request) {
		readBack = sm.GetString(r.Context(), "greeting")
	})

	h := newSessionRoundTripHarness(t, sm, mux)
	h.get(t, "/write")
	h.get(t, "/destroy")
	h.get(t, "/read")

	if readBack != "" {
		t.Errorf("GetString after Destroy = %q, want empty (the row must actually be deleted)", readBack)
	}
}

// TestNew_IterateVisitsEveryActiveSession proves identityStore.AllCtx (via
// sm.Iterate): two independent sessions written through two independent
// cookie jars are both visited and destroyable, which is exactly what
// identity/adapter's SessionRevoker depends on for RevokeAll.
func TestNew_IterateVisitsEveryActiveSession(t *testing.T) {
	sm := newGatedSessionManager(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /write", func(_ http.ResponseWriter, r *http.Request) {
		sm.Put(r.Context(), "marker", r.URL.Query().Get("marker"))
	})

	server := httptest.NewServer(sm.LoadAndSave(mux))
	t.Cleanup(server.Close)

	for _, marker := range []string{"a", "b"} {
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatalf("cookiejar.New: %v", err)
		}
		client := &http.Client{Jar: jar}
		resp, err := client.Get(server.URL + "/write?marker=" + marker)
		if err != nil {
			t.Fatalf("GET /write?marker=%s: %v", marker, err)
		}
		_ = resp.Body.Close()
	}

	visited := make(map[string]bool)
	err := sm.Iterate(t.Context(), func(ctx context.Context) error {
		visited[sm.GetString(ctx, "marker")] = true
		return nil
	})
	if err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	if !visited["a"] || !visited["b"] {
		t.Errorf("Iterate visited %v, want both %q and %q", visited, "a", "b")
	}
}
