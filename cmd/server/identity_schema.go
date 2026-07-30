package main

import (
	"context"
	"fmt"
	"net/url"

	corecfg "github.com/ericfisherdev/nestcore/config"
	ncmigrate "github.com/ericfisherdev/nestcore/db/migrate"
	identitymigrate "github.com/ericfisherdev/nestcore/identity/migrate"

	ownmigrate "github.com/ericfisherdev/nestorage/internal/platform/db/migrate"
)

// defaultIdentitySchemaName is the only DB_SCHEMA_IDENTITY value nestcore's
// identity/migrate package actually migrates against today — its schema
// name is a compile-time constant, not wired to corecfg.SchemaConfig (see
// that type's own doc). ensureSharedIdentitySchema rejects any other
// configured value rather than half-honor it.
const defaultIdentitySchemaName = "identity"

// ensureSharedIdentitySchema implements NSTR-123's startup compatibility
// check against nestcore's shared identity schema (epic NSTR-112), called
// from serve() before the main connection pool is built. It never migrates
// DOWN, and it never applies identity migrations without first confirming
// the shape it is looking at — the four outcomes it distinguishes:
//
//   - identity absent, and Nestorage has never migrated its own schema
//     either (a fresh install): bootstrap identity by running its
//     migrations. Two apps racing this on the same fresh database are safe —
//     identitymigrate.New's Runner holds a Postgres session-level advisory
//     lock for the duration of Up.
//   - identity absent, but Nestorage HAS migrated its own schema before (this
//     is not a fresh install): the operator pointed DATABASE_URL at a
//     database that was supposed to already be the shared one, and it is
//     not — readiness fails with an actionable error naming the schema and
//     host, never the DSN itself (which may carry credentials).
//   - identity present, at any version: apply whatever pending migrations
//     this binary embeds. A schema NEWER than this binary is deliberately
//     allowed rather than refused — see upgradeIdentity's own doc for why.
//
// Whether Nestorage has "migrated its own schema before" is read from its
// OWN goose version table (via ownmigrate's Runner), not from a
// schemas.Nestorage schema existing — Nestorage's own tables are not yet
// namespaced into a dedicated schema (a separate, not-yet-scheduled
// migration), so schema existence would never distinguish anything. Applied
// version already means exactly "has this app booted and migrated against
// this database before", independent of which schema its tables live in.
func ensureSharedIdentitySchema(ctx context.Context, dsn string, schemas corecfg.SchemaConfig) error {
	// nestcore's identity/migrate hard-codes its schema name (see
	// corecfg.SchemaConfig's own doc), so a configured value other than the
	// default would be probed here but never migrated against — refuse
	// rather than silently diverge into an unreachable state.
	if schemas.Identity != defaultIdentitySchemaName {
		return fmt.Errorf("DB_SCHEMA_IDENTITY=%q is not supported yet: nestcore's identity migrations are hard-coded to %q; leave it unset",
			schemas.Identity, defaultIdentitySchemaName)
	}

	identityExists, err := ncmigrate.SchemaExists(ctx, dsn, schemas.Identity)
	if err != nil {
		return fmt.Errorf("probe identity schema: %w", err)
	}

	identityRunner, err := identitymigrate.New()
	if err != nil {
		return err
	}

	if !identityExists {
		return bootstrapOrRefuseMissingIdentity(ctx, dsn, schemas, identityRunner)
	}
	return upgradeIdentity(ctx, dsn, identityRunner)
}

// bootstrapOrRefuseMissingIdentity handles the two "identity schema absent"
// outcomes: bootstrap on a genuinely fresh install, or fail readiness when
// Nestorage has run against this database before and the shared schema it
// depends on is unexpectedly gone.
func bootstrapOrRefuseMissingIdentity(ctx context.Context, dsn string, schemas corecfg.SchemaConfig, identityRunner *ncmigrate.Runner) error {
	ownRunner, err := ownmigrate.New()
	if err != nil {
		return err
	}
	ownVersion, err := ownRunner.AppliedVersion(ctx, dsn)
	if err != nil {
		return fmt.Errorf("check nestorage's own schema version: %w", err)
	}
	if ownVersion > 0 {
		return fmt.Errorf("identity schema %q not found on %s; expected an existing shared database (this app has already migrated against it before)",
			schemas.Identity, dsnHost(dsn))
	}

	if err := identityRunner.Up(ctx, dsn); err != nil {
		return fmt.Errorf("bootstrap identity schema: %w", err)
	}
	return nil
}

// upgradeIdentity applies whatever identity migrations this binary embeds
// and the database has not applied yet. A schema NEWER than this binary is
// deliberately allowed rather than refused: identity/migrate's additive-only
// discipline guarantees nothing an older binary depends on is ever removed
// or changed, and refusing would make a Nestova-first deploy an outage for
// every Nestorage instance still built against the older nestcore — see
// identity/migrate's own package doc ("whichever app deploys first migrates
// identity forward, and the other app... must keep working against the
// newer one") and RequireVersion's doc ("a newer-than-built schema is
// explicitly allowed"). Up is a no-op when nothing is pending, so this also
// covers the already-current case; probing the version first would only add
// contention against identityRunner's session lock for no behavioral gain.
func upgradeIdentity(ctx context.Context, dsn string, identityRunner *ncmigrate.Runner) error {
	if err := identityRunner.Up(ctx, dsn); err != nil {
		return fmt.Errorf("apply pending identity migrations: %w", err)
	}
	return nil
}

// dsnHost returns dsn's host (and port, if present) for use in an error
// message, never the DSN itself — which may carry a username and password.
// Falls back to a generic phrase when dsn cannot be parsed as a URL or
// carries no host, rather than risk echoing something credential-shaped.
func dsnHost(dsn string) string {
	if u, err := url.Parse(dsn); err == nil && u.Host != "" {
		return u.Host
	}
	return "the configured database"
}
