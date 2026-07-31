package main

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestVerifyOwnSchema_MockPoolError confirms verifyOwnSchema wraps a query
// failure rather than swallowing it, without needing a live database:
// pgxpool.New never dials eagerly, so a pool built over a well-formed but
// unreachable DSN fails on the first real query, exactly like the
// current_schema() check itself would against a bad connection.
func TestVerifyOwnSchema_MockPoolError(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/nope")
	if err != nil {
		t.Fatalf("pgxpool.New(): %v", err)
	}
	defer pool.Close()

	err = verifyOwnSchema(context.Background(), pool, "nestorage")
	if err == nil {
		t.Fatal("verifyOwnSchema() over an unreachable pool = nil error, want error")
	}
	if !strings.Contains(err.Error(), "verify current schema") {
		t.Errorf("error = %q, want it to name the current-schema check", err.Error())
	}
}
