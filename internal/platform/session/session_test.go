package session_test

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	corecfg "github.com/ericfisherdev/nestcore/config"

	"github.com/ericfisherdev/nestorage/internal/platform/session"
)

// newTestManager returns an scs.SessionManager over the library's default
// in-memory store — no Postgres needed. CSRFToken/VerifyCSRF only touch the
// session data scs.LoadAndSave attaches to the request context, not the
// storage backend, so the in-memory store exercises them identically to
// pgxstore. New's own pool wiring is covered separately by
// TestNew_AppliesCookieSettings and the gated end-to-end wizard test.
func newTestManager() *scs.SessionManager {
	return scs.New()
}

func TestCSRFToken_StableAcrossCalls(t *testing.T) {
	sm := newTestManager()
	var first, second string
	handler := sm.LoadAndSave(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		first = session.CSRFToken(r.Context(), sm)
		second = session.CSRFToken(r.Context(), sm)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if first == "" {
		t.Fatal("CSRFToken returned an empty token")
	}
	if first != second {
		t.Errorf("CSRFToken returned %q then %q within the same request, want the same token both times", first, second)
	}
}

func TestCSRFToken_DifferentSessionsGetDifferentTokens(t *testing.T) {
	sm := newTestManager()
	tokens := make(chan string, 2)
	handler := sm.LoadAndSave(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		tokens <- session.CSRFToken(r.Context(), sm)
	}))
	server := httptest.NewServer(handler)
	defer server.Close()

	// Two independent http.Get calls with no shared cookie jar, so each
	// request starts a brand-new session.
	for range 2 {
		resp, err := http.Get(server.URL)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		_ = resp.Body.Close()
	}
	a, b := <-tokens, <-tokens
	if a == b {
		t.Errorf("two independent sessions both got token %q, want distinct tokens", a)
	}
}

// csrfHarness drives VerifyCSRF over a real HTTP round trip, with a cookie
// jar, so the session cookie set by GET /token is the one read back on POST
// /verify — the property that makes VerifyCSRF's contract meaningful.
type csrfHarness struct {
	server *httptest.Server
	client *http.Client
	result chan bool
}

func newCSRFHarness(t *testing.T) *csrfHarness {
	t.Helper()
	sm := scs.New()
	result := make(chan bool, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(session.CSRFToken(r.Context(), sm)))
	})
	mux.HandleFunc("POST /verify", func(_ http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		result <- session.VerifyCSRF(r, sm)
	})

	server := httptest.NewServer(sm.LoadAndSave(mux))
	t.Cleanup(server.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	return &csrfHarness{server: server, client: &http.Client{Jar: jar}, result: result}
}

func (h *csrfHarness) fetchToken(t *testing.T) string {
	t.Helper()
	resp, err := h.client.Get(h.server.URL + "/token")
	if err != nil {
		t.Fatalf("GET /token: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 128)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}

// verify POSTs to /verify carrying header as X-CSRF-Token (when non-empty)
// and form as the request body, returning what the handler's VerifyCSRF
// call observed.
func (h *csrfHarness) verify(t *testing.T, header string, form url.Values) bool {
	t.Helper()
	if form == nil {
		form = url.Values{}
	}
	req, err := http.NewRequest(http.MethodPost, h.server.URL+"/verify", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if header != "" {
		req.Header.Set("X-CSRF-Token", header)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("POST /verify: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return <-h.result
}

func TestVerifyCSRF_HeaderMatch(t *testing.T) {
	h := newCSRFHarness(t)
	token := h.fetchToken(t)
	if !h.verify(t, token, nil) {
		t.Error("VerifyCSRF with the correct X-CSRF-Token header = false, want true")
	}
}

func TestVerifyCSRF_FormFieldFallback(t *testing.T) {
	h := newCSRFHarness(t)
	token := h.fetchToken(t)
	if !h.verify(t, "", url.Values{"csrf_token": {token}}) {
		t.Error("VerifyCSRF with the correct csrf_token form field = false, want true")
	}
}

func TestVerifyCSRF_Mismatch(t *testing.T) {
	h := newCSRFHarness(t)
	h.fetchToken(t)
	if h.verify(t, "not-the-right-token", nil) {
		t.Error("VerifyCSRF with a wrong token = true, want false")
	}
}

func TestVerifyCSRF_EmptySessionToken(t *testing.T) {
	sm := scs.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	ctx, err := sm.Load(req.Context(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	req = req.WithContext(ctx)
	if session.VerifyCSRF(req, sm) {
		t.Error("VerifyCSRF against a session with no CSRF token = true, want false")
	}
}

func TestNew_AppliesCookieSettings(t *testing.T) {
	cfg := corecfg.SessionConfig{
		Secret:   "fixture-secret-at-least-32-bytes-long",
		Secure:   true,
		Lifetime: 12 * time.Hour,
	}
	// pgxpool.New parses the DSN but never dials it (matching
	// cmd/server/main_test.go's TestReadiness), so this exercises New's
	// cookie/lifetime wiring without a real database. Pool-backed
	// persistence itself is exercised by the gated end-to-end wizard test.
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/nope?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()
	sm := session.New(pool, cfg)

	if !sm.Cookie.HttpOnly {
		t.Error("Cookie.HttpOnly = false, want true")
	}
	if sm.Cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("Cookie.SameSite = %v, want SameSiteLaxMode", sm.Cookie.SameSite)
	}
	if sm.Cookie.Path != "/" {
		t.Errorf("Cookie.Path = %q, want %q", sm.Cookie.Path, "/")
	}
	if !sm.Cookie.Persist {
		t.Error("Cookie.Persist = false, want true")
	}
	if sm.Cookie.Secure != cfg.Secure {
		t.Errorf("Cookie.Secure = %v, want %v", sm.Cookie.Secure, cfg.Secure)
	}
	if sm.Lifetime != cfg.Lifetime {
		t.Errorf("Lifetime = %v, want %v", sm.Lifetime, cfg.Lifetime)
	}
	if sm.IdleTimeout != cfg.Lifetime/2 {
		t.Errorf("IdleTimeout = %v, want half of Lifetime (%v)", sm.IdleTimeout, cfg.Lifetime/2)
	}
}
