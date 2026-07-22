package dbtest

import (
	"context"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/platform/db/migrate"
)

// TestHarnessConstructed proves the package-level Harness var actually
// builds. This mostly exercises newHarness()'s happy path (its panic branch
// fires only if the embedded migration set is ever empty, which cannot
// happen at runtime) but is worth asserting directly rather than trusting
// package initialization to have not panicked before any test ran.
func TestHarnessConstructed(t *testing.T) {
	if Harness == nil {
		t.Fatal("Harness is nil")
	}
}

// TestRunnerMigrator_ForwardsToRunner proves Reset and Up genuinely call
// through to the wrapped *ncmigrate.Runner rather than being dead adapter
// code. An unreachable loopback address still proves the call was attempted
// — connection refused surfaces immediately, without needing a real
// database.
func TestRunnerMigrator_ForwardsToRunner(t *testing.T) {
	runner, err := migrate.New()
	if err != nil {
		t.Fatalf("migrate.New(): %v", err)
	}
	m := runnerMigrator{runner: runner}
	ctx := context.Background()
	const unreachableDSN = "postgres://u:p@127.0.0.1:1/nope?sslmode=disable&connect_timeout=1"

	if err := m.Reset(ctx, unreachableDSN); err == nil {
		t.Error("Reset() against an unreachable DSN = nil error, want a connection error")
	}
	if err := m.Up(ctx, unreachableDSN); err == nil {
		t.Error("Up() against an unreachable DSN = nil error, want a connection error")
	}
}
