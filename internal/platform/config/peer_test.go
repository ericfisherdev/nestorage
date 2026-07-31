package config_test

import (
	"strings"
	"testing"

	corecfg "github.com/ericfisherdev/nestcore/config"

	"github.com/ericfisherdev/nestorage/internal/platform/config"
)

// basePeerEnv is the minimal environment Load needs to succeed, reused by
// every case below so each test only sets the PEER_* value it cares about.
func basePeerEnv() map[string]string {
	return map[string]string{
		"APP_ENV":      corecfg.EnvDev,
		"DATABASE_URL": "postgres://u:p@example.com:5432/nestorage?sslmode=disable",
	}
}

// TestLoad_PeerUnsetDefaultsEmpty covers NSTR-124's AC that a fresh install
// (no PEER_NESTOVA_URL set) needs zero configuration: NestovaURL defaults
// empty and Load succeeds, since an empty value is a valid "no peer
// installed" state rather than an error.
func TestLoad_PeerUnsetDefaultsEmpty(t *testing.T) {
	setEnv(t, basePeerEnv())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Peer.NestovaURL != "" {
		t.Errorf("Peer.NestovaURL = %q, want empty by default", cfg.Peer.NestovaURL)
	}
}

func TestLoad_PeerValidURLAccepted(t *testing.T) {
	env := basePeerEnv()
	env["PEER_NESTOVA_URL"] = "https://nestova.tailnet-name.ts.net"
	setEnv(t, env)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Peer.NestovaURL != "https://nestova.tailnet-name.ts.net" {
		t.Errorf("Peer.NestovaURL = %q, want the configured value", cfg.Peer.NestovaURL)
	}
}

func TestLoad_PeerMalformedURLRejected(t *testing.T) {
	env := basePeerEnv()
	env["PEER_NESTOVA_URL"] = "://not-a-url"
	setEnv(t, env)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "PEER_NESTOVA_URL") {
		t.Fatalf("Load() error = %v, want an error naming PEER_NESTOVA_URL", err)
	}
}

func TestLoad_PeerURLWithQueryRejected(t *testing.T) {
	env := basePeerEnv()
	env["PEER_NESTOVA_URL"] = "https://nestova.tailnet-name.ts.net?foo=bar"
	setEnv(t, env)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "PEER_NESTOVA_URL") {
		t.Fatalf("Load() error = %v, want an error naming PEER_NESTOVA_URL", err)
	}
}

func TestLoad_PeerURLWithFragmentRejected(t *testing.T) {
	env := basePeerEnv()
	env["PEER_NESTOVA_URL"] = "https://nestova.tailnet-name.ts.net#section"
	setEnv(t, env)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "PEER_NESTOVA_URL") {
		t.Fatalf("Load() error = %v, want an error naming PEER_NESTOVA_URL", err)
	}
}

func TestLoad_PeerNonHTTPSchemeRejected(t *testing.T) {
	env := basePeerEnv()
	env["PEER_NESTOVA_URL"] = "ftp://nestova.tailnet-name.ts.net"
	setEnv(t, env)

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "PEER_NESTOVA_URL") {
		t.Fatalf("Load() error = %v, want an error naming PEER_NESTOVA_URL", err)
	}
}
