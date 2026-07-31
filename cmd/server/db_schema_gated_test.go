package main

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	corecfg "github.com/ericfisherdev/nestcore/config"
	"github.com/ericfisherdev/nestcore/db"

	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
)

// TestVerifyOwnSchema_SearchPathPresent_Succeeds proves the happy path: a
// pool built over a DSN carrying the nestorage search_path option resolves
// current_schema() to it, exactly as dbtest.Harness's own derived DSNs do
// (NESTORAGE_TEST_DATABASE_URL is required to carry that option — see
// docs/testing.md), so this doubles as a check that the harness's own base
// DSN is configured correctly in this environment.
func TestVerifyOwnSchema_SearchPathPresent_Succeeds(t *testing.T) {
	pool := dbtest.Harness.NewIsolatedPool(t, "db_schema_ok")

	if err := verifyOwnSchema(context.Background(), pool, "nestorage"); err != nil {
		t.Fatalf("verifyOwnSchema() with the search_path option present = %v, want nil", err)
	}
}

// TestVerifyOwnSchema_SearchPathMissing_FailsWithActionableError proves the
// failure mode this guard exists for: a DSN with the search_path option
// stripped (simulating a forgotten options parameter in DATABASE_URL)
// resolves into public instead, and verifyOwnSchema must refuse rather than
// let the caller silently query — and later migrate — against the wrong
// schema.
func TestVerifyOwnSchema_SearchPathMissing_FailsWithActionableError(t *testing.T) {
	dsn := dbtest.Harness.DSN(t, "db_schema_missing")
	strippedDSN := stripSearchPathOption(t, dsn)

	pool, err := db.New(context.Background(), corecfg.DBConfig{DSN: strippedDSN, ConnTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("db.New() with the search_path option stripped: %v", err)
	}
	defer pool.Close()

	err = verifyOwnSchema(context.Background(), pool, "nestorage")
	if err == nil {
		t.Fatal("verifyOwnSchema() with the search_path option stripped = nil error, want error")
	}
	for _, want := range []string{`"public"`, `"nestorage"`, "search_path"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), want)
		}
	}
}

// stripSearchPathOption removes dsn's options query parameter entirely,
// standing in for a DATABASE_URL that never had the search_path option
// added — the exact operator mistake this guard catches.
func stripSearchPathOption(t *testing.T, dsn string) string {
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
