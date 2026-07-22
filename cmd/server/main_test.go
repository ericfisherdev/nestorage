package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	corecfg "github.com/ericfisherdev/nestcore/config"
)

func TestListenAndServe(t *testing.T) {
	t.Run("TLS configured: attempts to load the configured cert and key", func(t *testing.T) {
		// A free loopback port lets net.Listen succeed, so the failure
		// below can only come from ListenAndServeTLS's cert loading — the
		// plain-HTTP path never touches CertFile/KeyFile at all. No socket
		// is left listening: LoadX509KeyPair fails before Serve ever runs.
		srv := &http.Server{Addr: "127.0.0.1:0"}
		err := listenAndServe(srv, corecfg.TLSConfig{CertFile: "testdata-missing-cert.pem", KeyFile: "testdata-missing-key.pem"})
		if err == nil {
			t.Fatal("listenAndServe() error = nil, want a cert-loading error")
		}
		if !strings.Contains(err.Error(), "testdata-missing-cert.pem") {
			t.Errorf("listenAndServe() error = %v, want it to name the missing cert file (proves the TLS path was taken)", err)
		}
	})

	t.Run("TLS not configured: calls ListenAndServe, which honors graceful shutdown", func(t *testing.T) {
		// Close marks the server shut down, so both ListenAndServe and
		// ListenAndServeTLS return http.ErrServerClosed before ever calling
		// net.Listen — the plain-HTTP path is exercised without binding a
		// socket.
		srv := &http.Server{Addr: "127.0.0.1:0"}
		if err := srv.Close(); err != nil {
			t.Fatalf("srv.Close() error = %v", err)
		}
		err := listenAndServe(srv, corecfg.TLSConfig{})
		if !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("listenAndServe() = %v, want http.ErrServerClosed", err)
		}
	})
}

func TestReadiness(t *testing.T) {
	// pgxpool.New parses the DSN but does not connect, so no real database
	// is needed: Ping against a loopback port nothing listens on fails
	// immediately with a connection-refused error.
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/nope?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	defer pool.Close()

	if err := readiness(pool)(context.Background()); err == nil {
		t.Error("readiness()(ctx) = nil error, want an error for an unreachable database")
	}
}

func TestRun_ConfigError(t *testing.T) {
	// APP_ENV=prod with no DATABASE_URL fails config.Load before run() ever
	// touches the database or the HTTP server, mirroring the AC "missing or
	// invalid required config fails at startup with a clear message naming
	// the variable".
	t.Setenv("APP_ENV", corecfg.EnvProd)
	t.Setenv("DATABASE_URL", "")

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	err := run(logger)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("run() error = %v, want a DATABASE_URL configuration error", err)
	}
}
