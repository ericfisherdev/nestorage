package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// verifyOwnSchema fails loudly when pool's connection does not resolve
// current_schema() to want — NSTR-119's boot guard, called immediately after
// db.New returns. Two distinct misconfigurations produce this mismatch: a
// DATABASE_URL missing its search_path option (current_schema() resolves to
// public), or a fresh install whose server started before `make migrate-up`
// ran (nestorage does not exist yet, so it is also not first in the search
// path). Either must surface here, at boot, rather than scatter a fresh copy
// of every Nestorage table into public on the next migrate-up: nestcore's
// db/migrate.Runner (wired in internal/platform/db/migrate) always creates
// the nestorage schema via WithEnsureSchema, so a missing search_path option
// silently succeeds at every other layer.
func verifyOwnSchema(ctx context.Context, pool *pgxpool.Pool, want string) error {
	var got string
	// COALESCE: current_schema() is NULL when no schema in the search path
	// exists at all, which would otherwise fail Scan with a bare "cannot
	// scan NULL" instead of the actionable message below.
	if err := pool.QueryRow(ctx, "SELECT COALESCE(current_schema(), '')").Scan(&got); err != nil {
		return fmt.Errorf("verify current schema: %w", err)
	}
	if got != want {
		return fmt.Errorf(
			"current schema is %q, want %q: either DATABASE_URL is missing its search_path "+
				"option (append &options=-csearch_path%%3D%s%%2Cpublic to the DSN) or the %q "+
				"schema does not exist yet (run make migrate-up)",
			got, want, want, want,
		)
	}
	return nil
}
