package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestShellHandlers_StaticAssets(t *testing.T) {
	mux := testShellMux(t)
	req := httptest.NewRequest(http.MethodGet, "/static/css/app.css", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /static/css/app.css = %d, want %d", rec.Code, http.StatusOK)
	}
}
