package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	corecfg "github.com/ericfisherdev/nestcore/config"

	"github.com/ericfisherdev/nestorage/internal/platform/config"
)

// allKeys is every environment variable a loader reachable from Load reads,
// directly or through nestcore. Each test sets all of them (defaulting to
// "") so cases are isolated from the developer's ambient environment, not
// just from each other.
var allKeys = []string{
	"APP_ENV",
	"PORT", "TRUSTED_PROXIES", "SERVER_REQUEST_TIMEOUT", "PUBLIC_BASE_URL",
	"DATABASE_URL", "DB_MAX_CONNS", "DB_CONNECT_TIMEOUT", "DB_PROVIDER",
	"DB_POOL_MODE", "DB_SSL_ROOT_CERT", "MIGRATE_DATABASE_URL",
	"TLS_CERT_FILE", "TLS_KEY_FILE",
	"HSTS_ENABLED", "HSTS_MAX_AGE", "HSTS_INCLUDE_SUBDOMAINS", "HSTS_PRELOAD",
}

// setEnv isolates a test case from both the developer's ambient environment
// and any local .env file, mirroring nestcore's own config test helper.
// t.Chdir (like t.Setenv) auto-restores and forbids t.Parallel.
func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	t.Chdir(t.TempDir())
	for _, k := range allKeys {
		t.Setenv(k, env[k])
	}
}

// TestLoad_DevDSNFallback and its siblings below were originally table-driven
// subtests of one TestLoad; split into separate top-level functions so each
// case's setup and assertions read as one story instead of accumulating into
// a single function's cognitive complexity.

func TestLoad_DevDSNFallback(t *testing.T) {
	setEnv(t, map[string]string{"APP_ENV": corecfg.EnvDev})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if !strings.Contains(cfg.DB.DSN, "localhost:5433") {
		t.Errorf("DB.DSN = %q, want the dev fallback pointing at compose's port 5433", cfg.DB.DSN)
	}
	if cfg.Env != corecfg.EnvDev {
		t.Errorf("Env = %q, want %q", cfg.Env, corecfg.EnvDev)
	}
}

func TestLoad_ExplicitDatabaseURLWinsOverDevFallback(t *testing.T) {
	const explicit = "postgres://u:p@example.com:5432/nestorage?sslmode=disable"
	setEnv(t, map[string]string{"APP_ENV": corecfg.EnvDev, "DATABASE_URL": explicit})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.DB.DSN != explicit {
		t.Errorf("DB.DSN = %q, want %q", cfg.DB.DSN, explicit)
	}
}

func TestLoad_ProdWithNoDatabaseURLFails(t *testing.T) {
	setEnv(t, map[string]string{"APP_ENV": corecfg.EnvProd})

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("Load() error = %v, want an error naming DATABASE_URL", err)
	}
}

func TestLoad_AggregatesMultipleErrors(t *testing.T) {
	setEnv(t, map[string]string{
		"APP_ENV":                "staging",
		"DATABASE_URL":           "postgres://u:p@example.com:5432/nestorage?sslmode=disable",
		"SERVER_REQUEST_TIMEOUT": "not-a-duration",
	})

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want an aggregated configuration error")
	}
	if !strings.Contains(err.Error(), "APP_ENV") {
		t.Errorf("Load() error = %v, want it to name APP_ENV", err)
	}
	if !strings.Contains(err.Error(), "SERVER_REQUEST_TIMEOUT") {
		t.Errorf("Load() error = %v, want it to name SERVER_REQUEST_TIMEOUT", err)
	}
}

func TestLoad_DotenvReadInDevOnly(t *testing.T) {
	const fromEnvFile = "postgres://u:p@from-dotenv:5432/nestorage?sslmode=disable"

	t.Run("dev", func(t *testing.T) {
		setEnv(t, map[string]string{"APP_ENV": corecfg.EnvDev})
		// setEnv leaves DATABASE_URL set (to ""), and godotenv treats any
		// already-set key — even an empty one — as "do not overwrite" (it
		// checks presence in os.Environ(), not emptiness). Unsetting it
		// here, after t.Setenv already registered the restore, is the
		// standard way to test the "real environment wins" precedence
		// without giving DATABASE_URL an empty-but-present value that
		// would silently shadow the .env file.
		if err := os.Unsetenv("DATABASE_URL"); err != nil {
			t.Fatalf("os.Unsetenv(DATABASE_URL): %v", err)
		}
		writeDotenv(t, fromEnvFile)

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if cfg.DB.DSN != fromEnvFile {
			t.Errorf("DB.DSN = %q, want the value loaded from .env %q", cfg.DB.DSN, fromEnvFile)
		}
	})

	t.Run("prod", func(t *testing.T) {
		const explicit = "postgres://u:p@example.com:5432/nestorage?sslmode=disable"
		setEnv(t, map[string]string{"APP_ENV": corecfg.EnvProd, "DATABASE_URL": explicit})
		writeDotenv(t, fromEnvFile)

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
		if cfg.DB.DSN != explicit {
			t.Errorf("DB.DSN = %q, want the real environment value %q (.env must be ignored outside dev)", cfg.DB.DSN, explicit)
		}
	})
}

// writeDotenv writes a .env file into the current directory (set up by
// setEnv's t.Chdir) that sets DATABASE_URL to dsn.
func writeDotenv(t *testing.T, dsn string) {
	t.Helper()
	content := "DATABASE_URL=" + dsn + "\n"
	if err := os.WriteFile(filepath.Join(".", ".env"), []byte(content), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
}
