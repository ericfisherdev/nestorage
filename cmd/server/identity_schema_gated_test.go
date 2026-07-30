package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	corecfg "github.com/ericfisherdev/nestcore/config"
	ncmigrate "github.com/ericfisherdev/nestcore/db/migrate"
	identitymigrate "github.com/ericfisherdev/nestcore/identity/migrate"

	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
	ownmigrate "github.com/ericfisherdev/nestorage/internal/platform/db/migrate"
)

// testSchemas is the canonical default SchemaConfig every gated test below
// checks against — a fresh, unconfigured install.
var testSchemas = corecfg.SchemaConfig{Identity: "identity", Nestova: "nestova", Nestorage: "nestorage"}

// TestEnsureSharedIdentitySchema_FreshInstall_Bootstraps proves the first
// outcome: neither the identity schema nor Nestorage's own migrations exist
// yet, so the check bootstraps identity by running its migrations, and
// leaves it at its highest embedded version.
func TestEnsureSharedIdentitySchema_FreshInstall_Bootstraps(t *testing.T) {
	dsn := dbtest.Harness.DSN(t, "identity_boot_fresh")
	ctx := context.Background()

	if err := ensureSharedIdentitySchema(ctx, dsn, testSchemas); err != nil {
		t.Fatalf("ensureSharedIdentitySchema() on a fresh install = %v, want nil", err)
	}

	exists, err := schemaExistsForTest(ctx, t, dsn, "identity")
	if err != nil {
		t.Fatalf("check identity schema: %v", err)
	}
	if !exists {
		t.Fatal("identity schema was not created by the fresh-install bootstrap")
	}

	identityRunner := newIdentityRunner(t)
	statuses, err := identityRunner.Status(ctx, dsn)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, s := range statuses {
		if !s.Applied {
			t.Errorf("migration %s not applied after bootstrap", s.Source)
		}
	}
}

// TestEnsureSharedIdentitySchema_MissingButOwnSchemaMigrated_FailsReadiness
// proves the second outcome: Nestorage has migrated its own schema against
// this database before (simulated here directly, since Nestorage's own
// migrations do not run automatically at boot), but the identity schema is
// missing — the operator pointed at what should have been an existing
// shared database, and readiness must fail rather than silently bootstrap
// identity underneath already-live application data.
//
// UpTo(1) applies only Nestorage's own baseline migration — deliberately
// NOT Up(), which would run every migration including 00017, Nestorage's
// interim migration that itself creates the identity schema (predating
// nestcore's identity package) — that would make "own migrated" and
// "identity present" the same state, unable to exercise this outcome at
// all.
func TestEnsureSharedIdentitySchema_MissingButOwnSchemaMigrated_FailsReadiness(t *testing.T) {
	dsn := dbtest.Harness.DSN(t, "identity_boot_missing")
	ctx := context.Background()

	ownRunner, err := ownmigrate.New()
	if err != nil {
		t.Fatalf("ownmigrate.New(): %v", err)
	}
	if err := ownRunner.UpTo(ctx, dsn, 1); err != nil {
		t.Fatalf("apply nestorage's own baseline migration: %v", err)
	}
	t.Cleanup(func() {
		if err := ownRunner.Reset(ctx, dsn); err != nil {
			t.Logf("cleanup Reset failed: %v", err)
		}
	})

	err = ensureSharedIdentitySchema(ctx, dsn, testSchemas)
	if err == nil {
		t.Fatal("ensureSharedIdentitySchema() with identity missing on an already-migrated install = nil, want an error")
	}
	if !strings.Contains(err.Error(), "identity") {
		t.Errorf("error = %q, want it to name the identity schema", err.Error())
	}
	for _, credentialLike := range []string{"nestorage:nestorage", "@"} {
		if strings.Contains(err.Error(), credentialLike) {
			t.Errorf("error = %q, must not contain the DSN or its credentials", err.Error())
		}
	}

	exists, err := schemaExistsForTest(ctx, t, dsn, "identity")
	if err != nil {
		t.Fatalf("check identity schema: %v", err)
	}
	if exists {
		t.Error("identity schema must not be created on the failing readiness path")
	}
}

// TestEnsureSharedIdentitySchema_OlderIdentity_AppliesPending proves the
// third outcome: identity exists but is behind this binary's embedded
// migrations, so the check applies the pending ones and leaves the schema
// at its highest version.
func TestEnsureSharedIdentitySchema_OlderIdentity_AppliesPending(t *testing.T) {
	dsn := dbtest.Harness.DSN(t, "identity_boot_older")
	ctx := context.Background()

	identityRunner := newIdentityRunner(t)
	if err := identityRunner.Reset(ctx, dsn); err != nil {
		t.Fatalf("initial Reset: %v", err)
	}
	t.Cleanup(func() {
		if err := identityRunner.Reset(ctx, dsn); err != nil {
			t.Logf("cleanup Reset failed: %v", err)
		}
	})

	// Land one version behind the top so this run stays correct as
	// identity/migrate grows more migrations, rather than hard-coding "3".
	top := highestKnownVersion(ctx, t, identityRunner, dsn)
	if top < 2 {
		t.Fatalf("identity/migrate has only %d migrations; this test needs at least 2 to exercise a genuinely older schema", top)
	}
	if err := identityRunner.UpTo(ctx, dsn, top-1); err != nil {
		t.Fatalf("UpTo(%d): %v", top-1, err)
	}

	if err := ensureSharedIdentitySchema(ctx, dsn, testSchemas); err != nil {
		t.Fatalf("ensureSharedIdentitySchema() on an older identity schema = %v, want nil", err)
	}

	got, err := identityRunner.AppliedVersion(ctx, dsn)
	if err != nil {
		t.Fatalf("AppliedVersion: %v", err)
	}
	if got != top {
		t.Errorf("applied version after ensureSharedIdentitySchema = %d, want %d (every pending migration applied)", got, top)
	}
}

// TestEnsureSharedIdentitySchema_NewerIdentity_BootsWithoutMigratingDown
// proves the additive-only outcome: identity is at a version beyond
// anything this binary's embedded migrations know about (this binary was
// built against an older nestcore) — the check must boot successfully
// rather than refuse, per identity/migrate's documented additive-only
// contract (a newer-than-built schema is explicitly allowed; refusing would
// make an independent Nestova-first deploy an outage for every Nestorage
// instance still on the older nestcore). Critically, it must never migrate
// down: every table belonging to the (fabricated) newer version must still
// exist afterward, and the fabricated version itself must be untouched.
func TestEnsureSharedIdentitySchema_NewerIdentity_BootsWithoutMigratingDown(t *testing.T) {
	dsn := dbtest.Harness.DSN(t, "identity_boot_newer")
	ctx := context.Background()

	identityRunner := newIdentityRunner(t)
	if err := identityRunner.Reset(ctx, dsn); err != nil {
		t.Fatalf("initial Reset: %v", err)
	}
	if err := identityRunner.Up(ctx, dsn); err != nil {
		t.Fatalf("Up: %v", err)
	}

	const fabricatedVersion = 999999
	db := fabricateNewerAppliedVersion(ctx, t, dsn, identityRunner, fabricatedVersion)

	if err := ensureSharedIdentitySchema(ctx, dsn, testSchemas); err != nil {
		t.Fatalf("ensureSharedIdentitySchema() with identity newer than embedded = %v, want nil (additive-only: newer is allowed)", err)
	}

	// Never migrate down: every table from the real (non-fabricated)
	// migration set must still be present.
	for _, table := range []string{"household", "member", "sessions"} {
		var name *string
		if err := db.QueryRowContext(ctx, `SELECT to_regclass('identity.' || $1)`, table).Scan(&name); err != nil {
			t.Fatalf("query to_regclass(%q): %v", table, err)
		}
		if name == nil {
			t.Errorf("identity.%s no longer exists after booting against a newer schema — a down-migration ran", table)
		}
	}
	if got, err := identityRunner.AppliedVersion(ctx, dsn); err != nil {
		t.Fatalf("AppliedVersion: %v", err)
	} else if got != fabricatedVersion {
		t.Errorf("applied version = %d, want %d unchanged (no down-migration)", got, fabricatedVersion)
	}
}

// fabricateNewerAppliedVersion inserts a migration version beyond anything
// embedded here, standing in for a newer nestcore's identity schema — this
// binary has no source file for it, so this is the only way to construct
// the scenario. It registers a Cleanup that removes the fabricated row
// before resetting identityRunner: the row has no corresponding migration
// file, so goose cannot compute a down path through it, and Reset would
// fail with "version not found" and leave the row behind for the next run
// to trip over too. Returns the *sql.DB the caller queries afterward to
// confirm no down-migration ran; it is closed from within this Cleanup,
// not the caller's own defer, since Cleanup callbacks run AFTER the test
// function's own defers.
func fabricateNewerAppliedVersion(ctx context.Context, t *testing.T, dsn string, identityRunner *ncmigrate.Runner, version int) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO identity.goose_db_version (version_id, is_applied) VALUES ($1, true)", version,
	); err != nil {
		t.Fatalf("fabricate a newer applied version: %v", err)
	}
	t.Cleanup(func() {
		defer func() { _ = db.Close() }()
		if _, err := db.ExecContext(context.Background(),
			"DELETE FROM identity.goose_db_version WHERE version_id = $1", version,
		); err != nil {
			t.Logf("cleanup: remove fabricated version row failed: %v", err)
		}
		if err := identityRunner.Reset(context.Background(), dsn); err != nil {
			t.Logf("cleanup Reset failed: %v", err)
		}
	})
	return db
}

// newIdentityRunner returns a fresh identity/migrate Runner for one test's
// exclusive use.
func newIdentityRunner(t *testing.T) *ncmigrate.Runner {
	t.Helper()
	r, err := identitymigrate.New()
	if err != nil {
		t.Fatalf("identitymigrate.New(): %v", err)
	}
	return r
}

// highestKnownVersion returns the highest version identityRunner's own
// embedded migrations report, via Status — independent of what is currently
// applied in the database at dsn.
func highestKnownVersion(ctx context.Context, t *testing.T, identityRunner *ncmigrate.Runner, dsn string) int64 {
	t.Helper()
	statuses, err := identityRunner.Status(ctx, dsn)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var top int64
	for _, s := range statuses {
		if s.Version > top {
			top = s.Version
		}
	}
	return top
}

// schemaExistsForTest checks schema's presence directly, independent of
// ensureSharedIdentitySchema's own SchemaExists call, so the assertion is
// not circular.
func schemaExistsForTest(ctx context.Context, t *testing.T, dsn, schema string) (bool, error) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return false, err
	}
	defer func() { _ = db.Close() }()

	var exists bool
	err = db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)", schema).Scan(&exists)
	return exists, err
}
