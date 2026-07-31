package main

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
	"github.com/ericfisherdev/nestorage/internal/platform/db/migrate"
)

// TestVerifySearchPath_ConfiguredOption_Succeeds proves the happy path: a DSN
// carrying the nestorage search_path option (as NESTORAGE_TEST_DATABASE_URL
// itself must, see docs/testing.md) reports search_path resolving nestorage
// first, without needing the schema to exist yet — verifySearchPath checks
// the configured SHOW search_path setting, not current_schema(), precisely
// so it works before WithEnsureSchema has created anything.
func TestVerifySearchPath_ConfiguredOption_Succeeds(t *testing.T) {
	dsn := dbtest.Harness.DSN(t, "migrate_search_path_ok")

	if err := verifySearchPath(context.Background(), dsn, migrate.Schema); err != nil {
		t.Fatalf("verifySearchPath() with the search_path option present = %v, want nil", err)
	}
}

// TestVerifySearchPath_MissingOption_FailsWithActionableError proves the
// failure mode this guard exists for: a DSN with the search_path option
// stripped resolves to Postgres's own default search_path, which does not
// name nestorage first, and verifySearchPath must refuse rather than let a
// migrate command run its unqualified DDL against whatever schema that
// default resolves to.
func TestVerifySearchPath_MissingOption_FailsWithActionableError(t *testing.T) {
	dsn := dbtest.Harness.DSN(t, "migrate_search_path_missing")
	strippedDSN := stripSearchPathOptionForTest(t, dsn)

	err := verifySearchPath(context.Background(), strippedDSN, migrate.Schema)
	if err == nil {
		t.Fatal("verifySearchPath() with the search_path option stripped = nil error, want error")
	}
	if !strings.Contains(err.Error(), "nestorage") {
		t.Errorf("error = %q, want it to name the wanted schema", err.Error())
	}
}

// stripSearchPathOptionForTest removes dsn's options query parameter
// entirely, standing in for a DATABASE_URL/MIGRATE_DATABASE_URL that never
// had the search_path option added. Named distinctly from cmd/server's own
// stripSearchPathOption: these are two separate main packages, so nothing is
// shared between them.
func stripSearchPathOptionForTest(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	q := u.Query()
	q.Del("options")
	u.RawQuery = q.Encode()
	return u.String()
}
