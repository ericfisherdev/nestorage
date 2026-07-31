package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// verifyOwnSchema fails loudly when pool's connection does not resolve
// current_schema() to want — NSTR-119's boot guard, called immediately after
// db.New returns. A forgotten search_path option in DATABASE_URL must
// surface here, at boot, rather than scatter a fresh copy of every
// Nestorage table into public on the next migrate-up: nestcore's
// db/migrate.Runner (wired in internal/platform/db/migrate) always creates
// the nestorage schema via WithEnsureSchema, so a missing search_path option
// silently succeeds at every other layer.
func verifyOwnSchema(ctx context.Context, pool *pgxpool.Pool, want string) error {
	var got string
	if err := pool.QueryRow(ctx, "SELECT current_schema()").Scan(&got); err != nil {
		return fmt.Errorf("verify current schema: %w", err)
	}
	if got != want {
		return fmt.Errorf(
			"current schema is %q, want %q: DATABASE_URL is missing the search_path option "+
				"(add options=-c search_path=%s,public to the DSN)",
			got, want, want,
		)
	}
	return nil
}
