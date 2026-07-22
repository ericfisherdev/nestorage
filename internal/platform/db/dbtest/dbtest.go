// Package dbtest is Nestorage's application-side wiring over nestcore's
// shared database-gated test harness (github.com/ericfisherdev/nestcore/db/dbtest).
// nestcore owns no migrations of its own, so every consuming application
// wires its own Harness against its own migration runner; this is
// Nestorage's. See docs/testing.md for how to write a gated test against it.
package dbtest

import (
	"context"
	"fmt"

	ncdbtest "github.com/ericfisherdev/nestcore/db/dbtest"
	ncmigrate "github.com/ericfisherdev/nestcore/db/migrate"

	"github.com/ericfisherdev/nestorage/internal/platform/db/migrate"
)

// Harness runs Nestorage's database-gated tests, deriving an isolated
// database per package from NESTORAGE_TEST_DATABASE_URL. Every gated test
// package shares this Harness rather than constructing its own.
var Harness = newHarness()

func newHarness() *ncdbtest.Harness {
	runner, err := migrate.New()
	if err != nil {
		// The embedded migration set is fixed at build time, so a failure
		// here means the embed itself is broken — a programming error, not
		// a runtime condition any caller of Harness could recover from.
		panic(fmt.Sprintf("dbtest: %v", err))
	}
	return ncdbtest.New("NESTORAGE_TEST_DATABASE_URL", runnerMigrator{runner})
}

// runnerMigrator adapts *ncmigrate.Runner to nestcore's dbtest.Migrator
// interface. The adapter is load-bearing, not decorative: dbtest.Migrator
// declares Reset(ctx, dsn) error and Up(ctx, dsn) error, while the Runner's
// own methods take a trailing opts ...ncmigrate.Option, and Go requires an
// exact method signature to satisfy an interface — the variadic cannot be
// dropped by assignment. Do not "simplify" this away.
type runnerMigrator struct {
	runner *ncmigrate.Runner
}

func (m runnerMigrator) Reset(ctx context.Context, dsn string) error {
	return m.runner.Reset(ctx, dsn)
}

func (m runnerMigrator) Up(ctx context.Context, dsn string) error {
	return m.runner.Up(ctx, dsn)
}
