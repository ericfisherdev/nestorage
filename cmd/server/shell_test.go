package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	labelsdomain "github.com/ericfisherdev/nestorage/internal/labels/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/api"
	"github.com/ericfisherdev/nestorage/internal/platform/config"
	ratelimitmetrics "github.com/ericfisherdev/nestorage/internal/platform/metrics"
	storageapp "github.com/ericfisherdev/nestorage/internal/storage/app"
)

// generousRateLimiter builds a NSTR-58 KeyedRateLimiter that never denies
// within a single test's own handful of requests — these tests exercise
// newAPIRouteMount's auth/observability/fallback plumbing, not rate
// limiting itself (covered by identity/adapter's own ratelimit_test.go).
func generousRateLimiter() *identityadapter.KeyedRateLimiter {
	return identityadapter.NewKeyedRateLimiter(rate.Limit(1000), 1000)
}

// noAPIRoutes is the registerRoutes no-op these tests pass to
// newAPIRouteMount: they exercise the mount's own auth/observability/
// fallback plumbing, not any bounded context's specific routes (those are
// each covered by their own package's tests, e.g. storage/adapter's
// locations_api_test.go).
func noAPIRoutes(api.Registrar) {}

func TestNewShellHandlers_NilLogger(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("newShellHandlers(nil) did not panic")
		}
	}()
	newShellHandlers(nil)
}

func testShellMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	newShellHandlers(logger).Routes(mux)
	return mux
}

func TestShellHandlers_Root_RedirectsToBins(t *testing.T) {
	mux := testShellMux(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("GET / = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != "/bins" {
		t.Errorf("Location = %q, want %q", got, "/bins")
	}
}

// /bins itself is no longer served by shellHandlers (NSTR-31 moved it to
// storageadapter.BinsWebHandlers, gated behind RequireAuthenticated and
// requiring real query services) — its full-navigation-vs-HTMX-fragment
// split is covered by BinsWebHandlers' own hermetic tests
// (internal/storage/adapter/bins_web_test.go) instead of here.

// TestGuardedRoute_UnauthenticatedRequest_IdenticalRegardlessOfPathParamExistence
// covers NSTR-24's no-leak acceptance criterion at the mount point NSTR-21's
// admin routes and NSTR-23's /settings/api-key gate share: RequireAdmin
// denies before any handler or repository runs, so an unauthenticated
// request gets byte-identical status, headers, and body whether the path's
// {id} names a real user or not.
//
// Requests are marked HX-Request: true so Denier answers with its bare,
// fully generic 401 (see Denier's own doc) — a full-navigation 401 embeds
// the request's own path in its Location: /login?next=<path> redirect, which
// necessarily differs whenever the two paths do (existingID !=
// nonExistentID); that difference reflects which URL the client already
// typed, not whether the referenced resource exists, so it is not what this
// criterion is about.
func TestGuardedRoute_UnauthenticatedRequest_IdenticalRegardlessOfPathParamExistence(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	denier := identityadapter.NewDenier(logger)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/users/{id}/role", func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("RequireAdmin must deny before the route handler ever runs")
	})
	gated := identityadapter.RequireAdmin(denier)(mux)

	existingID := domain.NewUserID().String()
	const nonExistentID = "00000000-0000-0000-0000-000000000000"

	existing := httptest.NewRecorder()
	existingReq := httptest.NewRequest(http.MethodPost, "/admin/users/"+existingID+"/role", nil)
	existingReq.Header.Set("HX-Request", "true")
	gated.ServeHTTP(existing, existingReq)

	nonExistent := httptest.NewRecorder()
	nonExistentReq := httptest.NewRequest(http.MethodPost, "/admin/users/"+nonExistentID+"/role", nil)
	nonExistentReq.Header.Set("HX-Request", "true")
	gated.ServeHTTP(nonExistent, nonExistentReq)

	if existing.Code != nonExistent.Code {
		t.Fatalf("status differs: %d (existing id) vs %d (non-existent id)", existing.Code, nonExistent.Code)
	}
	if existing.Body.String() != nonExistent.Body.String() {
		t.Error("body differs between an existing and a non-existent path id")
	}
	for _, h := range []string{"Content-Type", "Location", "HX-Redirect"} {
		if existing.Header().Get(h) != nonExistent.Header().Get(h) {
			t.Errorf("header %q differs between an existing and a non-existent path id", h)
		}
	}
}

// testAPIMux mounts newAPIRouteMount at "/api/v1/" the same way newAppRoutes
// does, with a nil *api.Metrics — Observe's own nil-passthrough convention
// (see its doc) — since these tests only assert on the HTTP response, not
// on emitted metrics (covered by internal/platform/api's own tests).
func testAPIMux(t *testing.T) *http.ServeMux {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	denier := identityadapter.NewDenier(logger)
	rateLimitMetrics := ratelimitmetrics.NewRateLimitMetrics(prometheus.NewRegistry(), "test")
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", newAPIRouteMount(denier, nil, generousRateLimiter(), rateLimitMetrics, logger, noAPIRoutes))
	return mux
}

func TestAPIMount_NoAuthorizationHeader_Returns401JSON(t *testing.T) {
	mux := testAPIMux(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/anything with no Authorization header = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
}

// alwaysResolves is a minimal identityadapter.Resolver that unconditionally
// resolves to a fixed principal — enough to drive a request past
// identityadapter.Resolve without the real session/device-token/api-key
// resolvers, each already covered by identity/adapter's own tests.
type alwaysResolves struct{}

func (alwaysResolves) Resolve(_ context.Context, _ *http.Request) (domain.Principal, bool, error) {
	return domain.NewIntegrationPrincipal("test"), true, nil
}

// neverResolves is the fixed-absent counterpart to alwaysResolves, filling
// the chain slots a given test's bearer prefix never dispatches to.
type neverResolves struct{}

func (neverResolves) Resolve(_ context.Context, _ *http.Request) (domain.Principal, bool, error) {
	return domain.Principal{}, false, nil
}

func TestAPIMount_UnknownPathWithValidBearer_Returns404JSON(t *testing.T) {
	// Mirrors production wiring: identityadapter.Resolve is a GLOBAL
	// middleware (cmd/server/main.go's Middleware slice) that runs before
	// the request ever reaches the "/api/v1/" mount, so this composes it
	// the same way — the mux itself carries only newAPIRouteMount, which is
	// what actually implements the AC under test (an unknown path answers
	// JSON, not net/http's own plain-text 404).
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	denier := identityadapter.NewDenier(logger)
	chain := identityadapter.NewChain(neverResolves{}, neverResolves{}, alwaysResolves{})
	resolve := identityadapter.Resolve(chain, denier, logger)
	rateLimitMetrics := ratelimitmetrics.NewRateLimitMetrics(prometheus.NewRegistry(), "test")

	mux := http.NewServeMux()
	mux.Handle("/api/v1/", newAPIRouteMount(denier, nil, generousRateLimiter(), rateLimitMetrics, logger, noAPIRoutes))
	handler := resolve(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/no-such-route", nil)
	req.Header.Set("Authorization", "Bearer "+domain.APIKeyPrefix+"whatever")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/no-such-route = %d, want %d", rec.Code, http.StatusNotFound)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error.Code != "not_found" {
		t.Errorf("code = %q, want %q", body.Error.Code, "not_found")
	}
}

func TestShellHandlers_StaticAssets(t *testing.T) {
	mux := testShellMux(t)
	req := httptest.NewRequest(http.MethodGet, "/static/css/app.css", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /static/css/app.css = %d, want %d", rec.Code, http.StatusOK)
	}
}

// stubMembers/stubBins/stubLocations are minimal no-op implementations of
// shellDataService's own ports, only enough to satisfy newShellDataService's
// non-nil checks — the tests below exercise Peer, not Owners/Stats.
type stubMembers struct{}

func (stubMembers) List(context.Context) ([]domain.User, error) { return nil, nil }

type stubBins struct{}

func (stubBins) ListVisible(context.Context, domain.Principal) ([]storageapp.BinView, error) {
	return nil, nil
}

type stubLocations struct{}

func (stubLocations) List(context.Context, domain.Principal) ([]storageapp.LocationSummary, error) {
	return nil, nil
}

// countingPeerProbe records how many times Reachable was consulted, so a
// test can assert an unconfigured install probes zero times.
type countingPeerProbe struct {
	calls     int
	reachable bool
}

func (c *countingPeerProbe) Reachable(context.Context) bool {
	c.calls++
	return c.reachable
}

func newTestShellDataService(t *testing.T, peerCfg config.PeerConfig, peerProbe shellPeerReachabilityChecker) *shellDataService {
	t.Helper()
	labels, err := labelsdomain.NewRegistry()
	if err != nil {
		t.Fatalf("labelsdomain.NewRegistry() error = %v", err)
	}
	return newShellDataService(stubMembers{}, stubBins{}, stubLocations{}, labels, peerCfg, peerProbe)
}

// TestShellDataService_Peer_UnconfiguredReturnsNilAndNeverProbes covers
// NSTR-124's own AC: with no PEER_NESTOVA_URL configured, the sidebar
// renders no cross-app control, and — the part a doc comment alone cannot
// guarantee — no probe traffic exists at all.
func TestShellDataService_Peer_UnconfiguredReturnsNilAndNeverProbes(t *testing.T) {
	probe := &countingPeerProbe{reachable: true}
	s := newTestShellDataService(t, config.PeerConfig{}, probe)

	if got := s.Peer(context.Background()); got != nil {
		t.Errorf("Peer() = %+v, want nil with no PEER_NESTOVA_URL configured", got)
	}
	if probe.calls != 0 {
		t.Errorf("Reachable calls = %d, want 0 — an unconfigured install must issue no probe traffic", probe.calls)
	}
}

func TestShellDataService_Peer_ConfiguredCarriesProbeVerdict(t *testing.T) {
	probe := &countingPeerProbe{reachable: false}
	cfg := config.PeerConfig{NestovaURL: "https://nestova.example.test"}
	s := newTestShellDataService(t, cfg, probe)

	got := s.Peer(context.Background())
	if got == nil {
		t.Fatal("Peer() = nil, want an entry when PEER_NESTOVA_URL is set")
	}
	if got.URL != cfg.NestovaURL || got.Name != nestovaPeerName || got.Reachable {
		t.Errorf("Peer() = %+v, want URL=%q Name=%q Reachable=false", got, cfg.NestovaURL, nestovaPeerName)
	}
	if probe.calls != 1 {
		t.Errorf("Reachable calls = %d, want exactly 1", probe.calls)
	}
}

func TestNewShellDataService_NilPeerProbePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("newShellDataService(..., nil peerProbe) did not panic")
		}
	}()
	labels, err := labelsdomain.NewRegistry()
	if err != nil {
		t.Fatalf("labelsdomain.NewRegistry() error = %v", err)
	}
	newShellDataService(stubMembers{}, stubBins{}, stubLocations{}, labels, config.PeerConfig{}, nil)
}
