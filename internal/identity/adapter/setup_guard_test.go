package adapter_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
)

// fakeUserExistence is a configurable firstUserChecker/existingUserChecker
// fake: hasAny controls HasAnyUser's return value, and calls counts how
// many times it was invoked, so the latch's "no database query after
// completion" guarantee is directly assertable.
type fakeUserExistence struct {
	hasAny bool
	err    error
	calls  int
}

func (f *fakeUserExistence) HasAnyUser(_ context.Context) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return f.hasAny, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func newGuardedMux(repo *fakeUserExistence) *http.ServeMux {
	mux := http.NewServeMux()
	guard := adapter.SetupGuard(repo, testLogger())
	mux.Handle("/", guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	return mux
}

func TestSetupGuard_NoAdmin_RedirectsToSetup(t *testing.T) {
	mux := newGuardedMux(&fakeUserExistence{hasAny: false})
	req := httptest.NewRequest(http.MethodGet, "/bins", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET /bins with no admin = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != "/setup" {
		t.Errorf("Location = %q, want /setup", got)
	}
}

func TestSetupGuard_NoAdmin_HTMXRequestGetsHXRedirect(t *testing.T) {
	mux := newGuardedMux(&fakeUserExistence{hasAny: false})
	req := httptest.NewRequest(http.MethodGet, "/bins", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("HTMX GET /bins with no admin = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/setup" {
		t.Errorf("HX-Redirect = %q, want /setup", got)
	}
}

func TestSetupGuard_ExemptPrefixes_NeverBlocked(t *testing.T) {
	for _, path := range []string{"/setup", "/static/css/app.css", "/healthz", "/readyz", "/metrics"} {
		t.Run(path, func(t *testing.T) {
			mux := newGuardedMux(&fakeUserExistence{hasAny: false})
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("GET %s with no admin = %d, want %d (exempt path)", path, rec.Code, http.StatusOK)
			}
		})
	}
}

func TestSetupGuard_HasAdmin_PassesThrough(t *testing.T) {
	mux := newGuardedMux(&fakeUserExistence{hasAny: true})
	req := httptest.NewRequest(http.MethodGet, "/bins", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /bins with an admin present = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestSetupGuard_LatchesAfterFirstCompletion_NoFurtherQueries(t *testing.T) {
	repo := &fakeUserExistence{hasAny: true}
	guard := adapter.SetupGuard(repo, testLogger())
	handler := guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for range 3 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/bins", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /bins = %d, want %d", rec.Code, http.StatusOK)
		}
	}
	if repo.calls != 1 {
		t.Errorf("HasAnyUser called %d times across 3 requests, want 1 (the latch should skip the database after the first)", repo.calls)
	}
}

func TestSetupGuard_RepositoryError_500(t *testing.T) {
	mux := newGuardedMux(&fakeUserExistence{err: errors.New("boom")})
	req := httptest.NewRequest(http.MethodGet, "/bins", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("GET /bins with a repository error = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
