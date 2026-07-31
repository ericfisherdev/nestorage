package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	"SESSION_SECRET", "SESSION_LIFETIME", "SESSION_COOKIE_SECURE",
	"MEDIA_ROOT", "MEDIA_MAX_UPLOAD_BYTES", "MEDIA_THUMB_MAX_EDGE", "MEDIA_STORAGE_BACKEND",
	"S3_ENDPOINT", "S3_REGION", "S3_BUCKET", "S3_ACCESS_KEY_ID",
	"S3_SECRET_ACCESS_KEY", "S3_USE_PATH_STYLE", "S3_PRESIGN_TTL",
	"EMAIL_ENABLED", "EMAIL_FROM_ADDRESS", "EMAIL_DISPATCH_INTERVAL",
	"EMAIL_DISPATCH_BATCH_SIZE", "EMAIL_MAX_ATTEMPTS",
	"SES_REGION", "SES_ACCESS_KEY_ID", "SES_SECRET_ACCESS_KEY",
	"API_RATE_LIMIT_RPS", "API_RATE_LIMIT_BURST",
	"AUTH_RATE_LIMIT_RPM", "AUTH_RATE_LIMIT_BURST",
	"DB_SCHEMA_IDENTITY", "DB_SCHEMA_NESTOVA", "DB_SCHEMA_NESTORAGE",
	"PEER_NESTOVA_URL",
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

// TestLoad_DevRequiresDatabaseURL and its siblings below were originally
// table-driven subtests of one TestLoad; split into separate top-level
// functions so each case's setup and assertions read as one story instead
// of accumulating into a single function's cognitive complexity.

// TestLoad_DevRequiresDatabaseURL asserts dev no longer applies a hardcoded
// fallback DSN: a missing DATABASE_URL fails Load the same way it does in
// test and prod, naming the variable in the aggregated error.
func TestLoad_DevRequiresDatabaseURL(t *testing.T) {
	setEnv(t, map[string]string{"APP_ENV": corecfg.EnvDev})

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("Load() error = %v, want an error naming DATABASE_URL", err)
	}
}

// TestLoad_DevRealEnvironmentOverridesDotenv asserts that within dev, a
// DATABASE_URL already set in the real environment wins over a conflicting
// value in .env — godotenv never overwrites a variable that is already
// present, so the real environment must be the one Load() surfaces.
func TestLoad_DevRealEnvironmentOverridesDotenv(t *testing.T) {
	const fromEnv = "postgres://u:p@example.com:5432/nestorage?sslmode=disable"
	const fromDotenv = "postgres://u:p@from-dotenv:5432/nestorage?sslmode=disable"
	setEnv(t, map[string]string{"APP_ENV": corecfg.EnvDev, "DATABASE_URL": fromEnv})
	writeDotenv(t, fromDotenv)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.DB.DSN != fromEnv {
		t.Errorf("DB.DSN = %q, want the real environment value %q (.env must not override it)", cfg.DB.DSN, fromEnv)
	}
}

func TestLoad_ProdWithNoDatabaseURLFails(t *testing.T) {
	setEnv(t, map[string]string{"APP_ENV": corecfg.EnvProd})

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("Load() error = %v, want an error naming DATABASE_URL", err)
	}
}

func TestLoad_SessionDefaultsWireThrough(t *testing.T) {
	setEnv(t, map[string]string{
		"APP_ENV":      corecfg.EnvDev,
		"DATABASE_URL": "postgres://u:p@example.com:5432/nestorage?sslmode=disable",
	})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Session.Secret != corecfg.DevSessionSecret {
		t.Errorf("Session.Secret = %q, want the dev default", cfg.Session.Secret)
	}
	if cfg.Session.Lifetime != 12*time.Hour {
		t.Errorf("Session.Lifetime = %v, want the 12h default", cfg.Session.Lifetime)
	}
}

func TestLoad_ProdRejectsDefaultSessionSecret(t *testing.T) {
	setEnv(t, map[string]string{
		"APP_ENV":      corecfg.EnvProd,
		"DATABASE_URL": "postgres://u:p@example.com:5432/nestorage?sslmode=disable",
	})

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "SESSION_SECRET") {
		t.Fatalf("Load() error = %v, want an error naming SESSION_SECRET", err)
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
		setEnv(t, map[string]string{
			"APP_ENV":        corecfg.EnvProd,
			"DATABASE_URL":   explicit,
			"SESSION_SECRET": "a-real-32-byte-plus-production-secret",
		})
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

// TestLoad_SchemaDefaultsWireThrough asserts the canonical install (no
// DB_SCHEMA_* set) resolves to the three default schema names.
func TestLoad_SchemaDefaultsWireThrough(t *testing.T) {
	setEnv(t, map[string]string{
		"APP_ENV":      corecfg.EnvDev,
		"DATABASE_URL": "postgres://u:p@example.com:5432/nestorage?sslmode=disable",
	})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	want := corecfg.SchemaConfig{Identity: "identity", Nestova: "nestova", Nestorage: "nestorage"}
	if cfg.Schemas != want {
		t.Errorf("Schemas = %+v, want %+v", cfg.Schemas, want)
	}
}

// TestLoad_InvalidSchemaNameFails asserts an invalid DB_SCHEMA_* value fails
// Load, naming the variable in the aggregated error.
func TestLoad_InvalidSchemaNameFails(t *testing.T) {
	setEnv(t, map[string]string{
		"APP_ENV":            corecfg.EnvDev,
		"DATABASE_URL":       "postgres://u:p@example.com:5432/nestorage?sslmode=disable",
		"DB_SCHEMA_IDENTITY": "Not-Valid",
	})

	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "DB_SCHEMA_IDENTITY") {
		t.Fatalf("Load() error = %v, want an error naming DB_SCHEMA_IDENTITY", err)
	}
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
