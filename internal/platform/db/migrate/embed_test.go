package migrate

import (
	"io/fs"
	"testing"
)

// TestMigrationsEmbedded is the direct check that migrations are compiled
// into the binary via go:embed rather than read from disk at runtime — it
// globs the embedded filesystem itself, not New()'s validated result.
//
// This is an internal test (package migrate) specifically so it can reach
// the unexported migrationsFS; the gated tests proving Up/Status/Reset live
// in migrate_test.go as an external test package (package migrate_test)
// instead, because they need internal/platform/db/dbtest.Harness, and
// dbtest imports this package to build it — an internal test here importing
// dbtest back would be an import cycle.
func TestMigrationsEmbedded(t *testing.T) {
	matches, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob embedded migrations: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no embedded .sql migrations found")
	}
}
