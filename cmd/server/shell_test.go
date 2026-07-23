package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

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

func TestShellHandlers_Bins_FullNavigation(t *testing.T) {
	mux := testShellMux(t)
	req := httptest.NewRequest(http.MethodGet, "/bins", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /bins = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="sidebar"`) {
		t.Error("full-navigation response missing the sidebar — should be wrapped in the shell")
	}
	if !strings.Contains(strings.ToLower(body), "<!doctype html>") {
		t.Error("full-navigation response missing the doctype")
	}
	if got := rec.Header().Get("Vary"); got != "HX-Request" {
		t.Errorf("Vary = %q, want %q", got, "HX-Request")
	}
}

func TestShellHandlers_Bins_HTMXFragment(t *testing.T) {
	mux := testShellMux(t)
	req := httptest.NewRequest(http.MethodGet, "/bins", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /bins (HTMX) = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<!DOCTYPE") || strings.Contains(body, "<html") {
		t.Error("HTMX fragment response was wrapped in the full shell")
	}
	if strings.Contains(body, `id="sidebar"`) {
		t.Error("HTMX fragment response includes the sidebar — should be the bare toolbar")
	}
	if got := rec.Header().Get("Vary"); got != "HX-Request" {
		t.Errorf("Vary = %q, want %q", got, "HX-Request")
	}
}

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

func TestShellHandlers_StaticAssets(t *testing.T) {
	mux := testShellMux(t)
	req := httptest.NewRequest(http.MethodGet, "/static/css/app.css", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /static/css/app.css = %d, want %d", rec.Code, http.StatusOK)
	}
}
