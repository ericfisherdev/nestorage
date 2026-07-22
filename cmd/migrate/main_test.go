package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfisherdev/nestcore/config"
)

func TestNextVersion(t *testing.T) {
	t.Run("empty dir starts at 1", func(t *testing.T) {
		got, err := nextVersion(t.TempDir())
		if err != nil {
			t.Fatalf("nextVersion() error: %v", err)
		}
		if got != 1 {
			t.Errorf("nextVersion() = %d, want 1", got)
		}
	})

	t.Run("increments past the highest existing number", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{"00001_baseline.sql", "00002_auth.sql", "notes.txt"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := nextVersion(dir)
		if err != nil {
			t.Fatalf("nextVersion() error: %v", err)
		}
		if got != 3 {
			t.Errorf("nextVersion() = %d, want 3", got)
		}
	})

	t.Run("missing dir is an error", func(t *testing.T) {
		if _, err := nextVersion(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
			t.Error("nextVersion() = nil error, want error for missing dir")
		}
	})

	t.Run("skips a numeric prefix too large to parse as int", func(t *testing.T) {
		dir := t.TempDir()
		// migrationFilePrefix only checks for a leading run of digits, so a
		// corrupted or hand-edited filename with an absurdly long numeric
		// prefix still matches the regexp but overflows strconv.Atoi's int
		// range. That entry must be skipped, not treated as an error.
		for _, name := range []string{"99999999999999999999999_overflow.sql", "00001_baseline.sql"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := nextVersion(dir)
		if err != nil {
			t.Fatalf("nextVersion() error: %v", err)
		}
		if got != 2 {
			t.Errorf("nextVersion() = %d, want 2 (overflow entry skipped)", got)
		}
	})
}

func TestCreateMigration(t *testing.T) {
	t.Run("writes a sequential, slugged goose file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "00001_baseline.sql"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}

		path, err := createMigration(dir, "Add Widgets!!")
		if err != nil {
			t.Fatalf("createMigration() error: %v", err)
		}
		if want := filepath.Join(dir, "00002_add_widgets.sql"); path != want {
			t.Errorf("path = %q, want %q", path, want)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read created file: %v", err)
		}
		if got := string(body); got != "-- +goose Up\n\n-- +goose Down\n" {
			t.Errorf("template = %q, want the goose Up/Down skeleton", got)
		}
	})

	t.Run("rejects a name with no usable characters", func(t *testing.T) {
		if _, err := createMigration(t.TempDir(), "!!!"); err == nil {
			t.Error("createMigration() = nil error, want error for empty slug")
		}
	})

	t.Run("propagates a nextVersion failure", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		if _, err := createMigration(missing, "add widgets"); err == nil {
			t.Error("createMigration() = nil error, want the underlying nextVersion error")
		}
	})
}

func TestRunUnknownCommand(t *testing.T) {
	err := run([]string{"frobnicate"})
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("run() error = %v, want an unknown-command error", err)
	}
}

func TestRunNoArgs(t *testing.T) {
	if err := run(nil); err == nil {
		t.Error("run() = nil error, want usage error for no args")
	}
}

// TestRunDatabaseCommandsFailWithoutConfig exercises run()'s configuration
// loading and aggregation for every database-backed command, without a
// database: an empty DATABASE_URL fails config.LoadDB()'s Validate() before
// run() ever constructs a Runner or attempts a connection.
func TestRunDatabaseCommandsFailWithoutConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	for _, command := range []string{"up", "down", "status", "reset"} {
		err := run([]string{command})
		if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
			t.Errorf("run([%q]) error = %v, want a DATABASE_URL configuration error", command, err)
		}
	}
}

// TestRunCreateWithUnusableName exercises run()'s "create" case on its
// error path. An unusable slug is rejected by createMigration before it
// touches any directory, so this needs neither a database nor a real
// migrations directory.
func TestRunCreateWithUnusableName(t *testing.T) {
	if err := run([]string{"create", "!!!"}); err == nil {
		t.Error(`run(["create", "!!!"]) = nil error, want error for an unusable migration name`)
	}
}

// TestRunStatusConnectionFailure exercises run()'s "status" case on its
// error path — Status() failing to connect — without a database: the DSN
// is syntactically valid so it passes config validation, but points at a
// loopback port nothing listens on, so the connection is refused
// immediately.
func TestRunStatusConnectionFailure(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@127.0.0.1:1/nope?sslmode=disable&connect_timeout=1")

	if err := run([]string{"status"}); err == nil {
		t.Error(`run(["status"]) against an unreachable database = nil error, want a connection error`)
	}
}

func TestMigrateSettings(t *testing.T) {
	const appDSN = "postgres://u:p@pooler.supabase.com:6543/postgres?sslmode=require"
	const migrateDSN = "postgres://u:p@db.supabase.com:5432/postgres?sslmode=require"

	t.Run("defaults to DATABASE_URL with no pooler-safe option", func(t *testing.T) {
		dsn, opts := migrateSettings(config.DBConfig{DSN: appDSN})
		if dsn != appDSN {
			t.Errorf("dsn = %q, want %q", dsn, appDSN)
		}
		if len(opts) != 0 {
			t.Errorf("opts = %d, want 0 (postgres / no override)", len(opts))
		}
	})

	t.Run("MIGRATE_DATABASE_URL wins and stays normal protocol", func(t *testing.T) {
		// A dedicated migrate DSN is assumed to be a session/direct connection,
		// so the pooler-safe simple protocol is not forced even in transaction
		// mode.
		dsn, opts := migrateSettings(config.DBConfig{
			DSN:        appDSN,
			MigrateDSN: migrateDSN,
			Provider:   config.DBProviderSupabase,
			PoolMode:   config.DBPoolModeTransaction,
		})
		if dsn != migrateDSN {
			t.Errorf("dsn = %q, want %q", dsn, migrateDSN)
		}
		if len(opts) != 0 {
			t.Errorf("opts = %d, want 0 (dedicated migrate DSN)", len(opts))
		}
	})

	t.Run("transaction pooler without a migrate DSN enables pooler-safe", func(t *testing.T) {
		dsn, opts := migrateSettings(config.DBConfig{
			DSN:      appDSN,
			Provider: config.DBProviderSupabase,
			PoolMode: config.DBPoolModeTransaction,
		})
		if dsn != appDSN {
			t.Errorf("dsn = %q, want %q", dsn, appDSN)
		}
		if len(opts) != 1 {
			t.Errorf("opts = %d, want 1 (pooler-safe)", len(opts))
		}
	})

	t.Run("supabase session mode does not force pooler-safe", func(t *testing.T) {
		_, opts := migrateSettings(config.DBConfig{
			DSN:      appDSN,
			Provider: config.DBProviderSupabase,
			PoolMode: config.DBPoolModeSession,
		})
		if len(opts) != 0 {
			t.Errorf("opts = %d, want 0 (session mode)", len(opts))
		}
	})
}

func TestRunRejectsTrailingArgs(t *testing.T) {
	// Validation happens before config loading, so these need no database.
	cases := [][]string{
		{"reset", "now"},     // trailing arg on a destructive DB command
		{"create"},           // missing name
		{"create", "a", "b"}, // extra arg would be silently dropped
	}
	for _, args := range cases {
		if err := run(args); err == nil {
			t.Errorf("run(%q) = nil error, want usage error", args)
		}
	}
}
