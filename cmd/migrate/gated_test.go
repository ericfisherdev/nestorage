package main

import (
	"testing"

	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
)

// TestRun_DatabaseCommands drives every database-backed subcommand through
// run() itself — not just the primitives it delegates to — against a real,
// isolated database. This is the CLI-level equivalent of the manual
// `go run ./cmd/migrate up|status|down|reset` walkthrough this ticket's
// acceptance criteria describe, made repeatable.
func TestRun_DatabaseCommands(t *testing.T) {
	dsn := dbtest.Harness.DSN(t, "migrate_cmd")
	t.Setenv("DATABASE_URL", dsn)

	if err := run([]string{"status"}); err != nil {
		t.Fatalf("status: %v", err)
	}
	if err := run([]string{"up"}); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := run([]string{"status"}); err != nil {
		t.Fatalf("status after up: %v", err)
	}
	if err := run([]string{"down"}); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := run([]string{"reset"}); err != nil {
		t.Fatalf("reset: %v", err)
	}
}
