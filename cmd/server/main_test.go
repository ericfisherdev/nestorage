package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	corecfg "github.com/ericfisherdev/nestcore/config"

	"github.com/ericfisherdev/nestorage/internal/platform/config"
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

// TestLogFederationMode drives logFederationMode against a buffer-backed
// slog JSON handler — no database needed — asserting the standalone line,
// the federated line naming the host and client id, and the absence of the
// secret string from the output in both cases (NSTR-100).
func TestLogFederationMode(t *testing.T) {
	t.Run("standalone", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, nil))

		logFederationMode(logger, config.ProviderConfig{})

		var line map[string]any
		if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
			t.Fatalf("decode log line: %v; raw: %s", err, buf.String())
		}
		if line["mode"] != "standalone" {
			t.Errorf(`log line "mode" = %v, want "standalone"`, line["mode"])
		}
		if _, ok := line["provider_host"]; ok {
			t.Error("standalone log line must not include provider_host")
		}
		if _, ok := line["client_id"]; ok {
			t.Error("standalone log line must not include client_id")
		}
	})

	t.Run("federated", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, nil))
		cfg := config.ProviderConfig{
			BaseURL:      "https://nestova.example",
			ClientID:     "nestorage-client",
			ClientSecret: "super-secret-value",
		}

		logFederationMode(logger, cfg)

		raw := buf.String()
		if strings.Contains(raw, "super-secret-value") {
			t.Fatalf("log line contains the client secret: %s", raw)
		}
		var line map[string]any
		if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
			t.Fatalf("decode log line: %v; raw: %s", err, raw)
		}
		if line["mode"] != "federated" {
			t.Errorf(`log line "mode" = %v, want "federated"`, line["mode"])
		}
		if line["provider_host"] != "nestova.example" {
			t.Errorf(`log line "provider_host" = %v, want "nestova.example"`, line["provider_host"])
		}
		if line["client_id"] != "nestorage-client" {
			t.Errorf(`log line "client_id" = %v, want "nestorage-client"`, line["client_id"])
		}
	})
}
