// package migrate_test (an external test package, not package migrate) is
// deliberate: this test needs internal/platform/db/dbtest.Harness, and
// dbtest imports this package (migrate) to build it, so a test compiled AS
// package migrate importing dbtest back would be an import cycle. The
// ungated check that needs the package's unexported migrationsFS lives
// separately in embed_test.go, as package migrate, for the same reason in
// reverse.
package migrate_test

import (
	"context"
	"database/sql"
	"testing"

	ncmigrate "github.com/ericfisherdev/nestcore/db/migrate"

	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
	"github.com/ericfisherdev/nestorage/internal/platform/db/migrate"
)

// TestMigrate_UpStatusDownToReset is Nestorage's own proof, over its real
// embedded migration set and nestcore's real Runner, of what a hermetic unit
// test cannot reach: Up actually applies the baseline against Postgres,
// Status reports it applied, DownTo(0) and Reset return to empty, and
// re-applying Up with nothing pending is idempotent. It also checks the
// pgcrypto extension directly rather than trusting goose's own bookkeeping
// alone, so a Status mismatch and a schema mismatch can't both hide behind
// the same passing assertion.
func TestMigrate_UpStatusDownToReset(t *testing.T) {
	dsn := dbtest.Harness.DSN(t, "migrate")
	ctx := context.Background()

	runner, err := migrate.New()
	if err != nil {
		t.Fatalf("migrate.New(): %v", err)
	}

	if err := runner.Reset(ctx, dsn); err != nil {
		t.Fatalf("initial Reset: %v", err)
	}
	t.Cleanup(func() {
		if err := runner.Reset(ctx, dsn); err != nil {
			t.Logf("cleanup Reset failed: %v", err)
		}
	})

	if err := runner.Up(ctx, dsn); err != nil {
		t.Fatalf("Up: %v", err)
	}
	requireAllApplied(ctx, t, runner, dsn, true)
	if !pgcryptoInstalled(t, dsn) {
		t.Error("pgcrypto extension not installed after Up")
	}

	// Up must be idempotent: re-applying with nothing pending must not error.
	if err := runner.Up(ctx, dsn); err != nil {
		t.Fatalf("second Up (idempotency): %v", err)
	}
	requireAllApplied(ctx, t, runner, dsn, true)

	if err := runner.DownTo(ctx, dsn, 0); err != nil {
		t.Fatalf("DownTo(0): %v", err)
	}
	requireAllApplied(ctx, t, runner, dsn, false)
	if pgcryptoInstalled(t, dsn) {
		t.Error("pgcrypto extension still installed after DownTo(0)")
	}

	if err := runner.Up(ctx, dsn); err != nil {
		t.Fatalf("Up after DownTo(0): %v", err)
	}
	if err := runner.Reset(ctx, dsn); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	requireAllApplied(ctx, t, runner, dsn, false)
}

// requireAllApplied fails the test unless every migration Status reports
// has Applied == want. It also fails on an empty result, since an empty
// slice would otherwise vacuously satisfy either want value.
func requireAllApplied(ctx context.Context, t *testing.T, runner *ncmigrate.Runner, dsn string, want bool) {
	t.Helper()
	statuses, err := runner.Status(ctx, dsn)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) == 0 {
		t.Fatal("Status() reported no migrations")
	}
	for _, s := range statuses {
		if s.Applied != want {
			t.Errorf("migration %s Applied = %v, want %v", s.Source, s.Applied, want)
		}
	}
}

// pgcryptoInstalled queries pg_extension directly, independent of goose's
// own bookkeeping, so this test also catches a baseline migration whose Up
// block silently does nothing.
func pgcryptoInstalled(t *testing.T, dsn string) bool {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	var exists bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pgcrypto')`).Scan(&exists); err != nil {
		t.Fatalf("query pg_extension: %v", err)
	}
	return exists
}
