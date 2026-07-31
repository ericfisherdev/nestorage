package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// verifySearchPath fails loudly when dsn's search_path does not resolve to
// want first — cmd/migrate's own boot guard (NSTR-119), checked before any
// command that executes migration SQL. The CREATE/DROP TABLE statements in
// internal/platform/db/migrate/migrations are unqualified, so they land
// wherever search_path resolves; cmd/server's own verifyOwnSchema never
// inspects this DSN, since migrateSettings prefers MIGRATE_DATABASE_URL over
// the DSN cmd/server connects with.
//
// This checks the configured search_path itself via SHOW, not
// current_schema(): on a fresh database, WithEnsureSchema has not yet
// created the nestorage schema when this runs, so current_schema() would
// resolve to public regardless of the DSN's options and reject a legitimate
// first `migrate up`.
func verifySearchPath(ctx context.Context, dsn, want string) error {
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open migrate connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	var raw string
	if err := conn.QueryRowContext(ctx, "SHOW search_path").Scan(&raw); err != nil {
		return fmt.Errorf("verify search_path: %w", err)
	}

	first := strings.Trim(strings.TrimSpace(strings.SplitN(raw, ",", 2)[0]), `"`)
	if first != want {
		return fmt.Errorf(
			"search_path is %q, want %q first: DATABASE_URL (or MIGRATE_DATABASE_URL) is missing "+
				"the search_path option (append &options=-csearch_path%%3D%s%%2Cpublic to the DSN)",
			raw, want, want,
		)
	}
	return nil
}
