package main

import (
	"context"
	"strings"
	"testing"

	corecfg "github.com/ericfisherdev/nestcore/config"
)

// TestEnsureSharedIdentitySchema_InvalidDSNPropagatesError confirms
// ensureSharedIdentitySchema fails fast on a malformed DSN, via the
// SchemaExists probe's own connect error, without needing a live database.
func TestEnsureSharedIdentitySchema_InvalidDSNPropagatesError(t *testing.T) {
	schemas := corecfg.SchemaConfig{Identity: "identity", Nestova: "nestova", Nestorage: "nestorage"}
	err := ensureSharedIdentitySchema(context.Background(), "://nope", schemas)
	if err == nil {
		t.Fatal("ensureSharedIdentitySchema() with an invalid DSN = nil error, want error")
	}
	if !strings.Contains(err.Error(), "probe identity schema") {
		t.Errorf("error = %q, want it to name the identity-schema probe step", err.Error())
	}
}

// TestEnsureSharedIdentitySchema_NonDefaultIdentitySchemaRejected confirms a
// configured DB_SCHEMA_IDENTITY other than the default is refused before any
// database call — nestcore's identity/migrate package hard-codes its schema
// name, so any other configured value would be probed but never migrated
// against, silently bricking boot on the next run (see
// corecfg.SchemaConfig's own doc). No live database is needed: the guard
// runs before ensureSharedIdentitySchema's first database call.
func TestEnsureSharedIdentitySchema_NonDefaultIdentitySchemaRejected(t *testing.T) {
	schemas := corecfg.SchemaConfig{Identity: "custom_identity", Nestova: "nestova", Nestorage: "nestorage"}
	err := ensureSharedIdentitySchema(context.Background(), "postgres://u:p@127.0.0.1:1/nope", schemas)
	if err == nil {
		t.Fatal("ensureSharedIdentitySchema() with a non-default DB_SCHEMA_IDENTITY = nil error, want error")
	}
	if !strings.Contains(err.Error(), "DB_SCHEMA_IDENTITY") {
		t.Errorf("error = %q, want it to name DB_SCHEMA_IDENTITY", err.Error())
	}
}

// TestDsnHost covers both of dsnHost's branches: a URL-form DSN with a host,
// and every input that must fall back to the generic phrase rather than risk
// echoing something credential-shaped.
func TestDsnHost(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "URL-form DSN reports host and port, not credentials",
			dsn:  "postgres://nestorage:supersecret@db.internal:5432/nest?sslmode=disable",
			want: "db.internal:5432",
		},
		{
			name: "unparsable DSN falls back to the generic phrase",
			dsn:  "://nope",
			want: "the configured database",
		},
		{
			name: "keyword/value DSN with no host falls back to the generic phrase",
			dsn:  "dbname=nest user=nestorage password=supersecret",
			want: "the configured database",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dsnHost(tt.dsn); got != tt.want {
				t.Errorf("dsnHost(%q) = %q, want %q", tt.dsn, got, tt.want)
			}
		})
	}
}
