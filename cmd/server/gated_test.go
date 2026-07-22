package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
)

// TestServe_Lifecycle drives serve() end to end against a real, isolated
// database: configuration loads, the pool connects, the HTTP server starts
// and answers /healthz and /readyz, and a cancelled context drains it
// cleanly. This is the automated equivalent of this ticket's acceptance
// criteria (`make run` against the compose database; /healthz and /readyz
// behavior; a clean SIGINT/SIGTERM exit) — see run's own doc for why this
// drives serve directly rather than sending a real OS signal to the test
// process.
func TestServe_Lifecycle(t *testing.T) {
	dsn := dbtest.Harness.DSN(t, "server")
	t.Setenv("DATABASE_URL", dsn)
	// test, not dev: dev would attempt to load a developer's own .env from
	// this package's directory, which this test must not depend on.
	t.Setenv("APP_ENV", "test")

	port := freePort(t)
	t.Setenv("PORT", fmt.Sprintf("%d", port))

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- serve(ctx, logger) }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitUntilHealthy(t, base+"/healthz")

	resp, err := http.Get(base + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /readyz = %d, want 200 (the database is reachable)", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve() = %v, want nil after a clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve() did not return within 5s of ctx cancellation")
	}
}

// freePort reserves an OS-assigned free TCP port on loopback and releases
// it immediately, for the server under test to bind next. Not fully race
// free against a concurrent bind, but standard practice for a single, local
// test binary.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a free port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// waitUntilHealthy polls url until it answers 200 or the deadline passes,
// bridging the small window between the server goroutine starting and the
// listener actually accepting connections.
func waitUntilHealthy(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not become healthy in time", url)
}
