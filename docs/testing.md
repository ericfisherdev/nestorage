# Testing

Three tiers: the default suite, which is hermetic and needs nothing; the
database-gated suite, which needs a real Postgres; and the S3-gated suite,
which needs a real S3-compatible endpoint (NSTR-35).

## The default suite

```sh
make test        # go test -race -cover ./...
```

No database, no network, no containers. Gated tests skip themselves when
`NESTORAGE_TEST_DATABASE_URL` is unset, which is what keeps this run
dependency-free.

## The database-gated suite

Nestorage's own `compose.yaml` no longer starts a Postgres service (NSTR-119
retired it): Nestorage's tables live in a `nestorage` schema of the "nest"
database shared with Nestova and identity, on the instance Nestova's own
`docker compose up -d` starts at `127.0.0.1:5432`. Start that first, then
create `nestorage_test` in the same instance once (it is a separate database,
not a schema, for the isolation reasons below):

```sh
createdb -h 127.0.0.1 -p 5432 -U nestova nestorage_test
export NESTORAGE_TEST_DATABASE_URL="postgres://nestova:nestova@127.0.0.1:5432/nestorage_test?sslmode=disable&options=-csearch_path%3Dnestorage%2Cpublic"
make test-gated
```

`createdb` connects directly over TCP rather than `docker compose exec` — the
Postgres container is owned by Nestova's own `compose.yaml`, a different
Compose project than this repo's, so `exec` run from a Nestorage worktree
would look for a `postgres` service here and fail (this repo's `compose.yaml`
only defines `minio`).

The `options` parameter carries `search_path=nestorage,public` (NSTR-119):
`migrate.Reset`/`Up` create the `nestorage` schema and land every migrated
object in it, exactly as production does, and `cmd/server`'s boot guard
(`verifyOwnSchema` in `cmd/server/db_schema.go`) refuses to connect without
it — a DSN missing this option fails every gated adapter test with a
`current schema` error naming the missing option.

`make test-gated` names the gated packages explicitly
(`GATED_TEST_PACKAGES` in the [`Makefile`](../Makefile)). `go test ./...`
with the variable set works too and runs everything; the explicit target
exists so a gated run is deliberate and its package list is reviewable.

### Prerequisites

- **A Postgres reachable at that DSN, version 17.** Production runs 17, and
  Nestova's `compose.yaml` pins the same major so the gated suite exercises
  what the appliance actually runs.
- **A database named `test` or ending in `_test`.** Enforced as a safety
  rail: the harness refuses to run otherwise, because it drops and
  recreates schemas. `nestorage_test` is the convention.
- **The `CREATEDB` privilege on that role.** The harness creates a database
  per package on demand. Nestova's compose service's `nestova` role already
  has it (the official postgres image grants `POSTGRES_USER` superuser); a
  purpose-made role needs it granted:

  ```sql
  ALTER ROLE <role> CREATEDB;
  ```

  `<role>` is whatever role your DSN authenticates as — `nestova` for the
  local recipe above, which already has it via `POSTGRES_USER`. Without
  `CREATEDB`, gated tests fail with a `create database` error naming this
  document.

### Isolation model

Every gated package gets **its own database**, derived from the configured
one by appending a package suffix — `nestorage_test` becomes
`nestorage_test_migrate`, and so on as further packages add gated suites.

That per-package database is what makes a parallel run safe. Go runs
different packages' test binaries concurrently, so a single shared database
would race: one package's schema reset could drop the schema out from under
another package's in-flight test.

Writing a gated test:

```go
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return dbtest.Harness.NewIsolatedPool(t, "bins")
}
```

`dbtest.Harness` (`internal/platform/db/dbtest`) — Nestorage's own wiring
over nestcore's shared harness — does the rest: skips when the environment
variable is unset, enforces the name safety rail, creates the derived
database if missing, resets and migrates it, and registers cleanup.

- The **suffix must be unique per package** and stable. Two packages
  sharing one would reintroduce exactly the race this removes.
- Need the connection string rather than a pool — a second pool in the same
  test, or a CLI invocation — use `dbtest.Harness.DSN(t, "<same-suffix>")`.
  Do not read `NESTORAGE_TEST_DATABASE_URL` directly: that names the *base*
  database, not the package's, so the two would silently diverge.

Derived databases persist between runs; only their schemas are reset (on
both setup and cleanup), so repeat runs are fast. Nestorage's own
`compose.yaml` no longer owns any Postgres storage to drop wholesale
(NSTR-119) — drop the derived databases directly instead:

```sql
-- inside psql, connected to the maintenance database. \gexec runs each
-- statement the SELECT generates; without it this only prints them.
SELECT format('DROP DATABASE %I;', datname)
  FROM pg_database
 WHERE datname LIKE 'nestorage\_test\_%' ESCAPE '\'
\gexec
```

### The one exception

`internal/platform/db/migrate/migrate_test.go` uses `dbtest.Harness.DSN`
for the connection string, but calls `Reset`/`Up`/`DownTo`/`Status` on its
own `*migrate.Runner` directly rather than going through
`dbtest.Harness.NewIsolatedPool` — this package tests the migration
primitives `NewIsolatedPool` is built on, so layering it over the very
thing it depends on would be backwards. `internal/platform/db/migrate/embed_test.go`
is a separate, ungated, internal test file (`package migrate`) for the same
reason in reverse: it needs the package's unexported embedded filesystem,
and `dbtest` imports `migrate` to build its `Harness`, so a `package
migrate` test importing `dbtest` back would be an import cycle.

## The S3-gated suite

`internal/media/adapter/photo_store_s3_test.go` self-skips unless
`NESTORAGE_TEST_S3_ENDPOINT` is set — a separate gate from
`NESTORAGE_TEST_DATABASE_URL`, so `make test` never depends on a running
S3-compatible endpoint. The `minio` service in
[`compose.yaml`](../compose.yaml) is the easiest way to get one:

```sh
docker compose up -d minio
export NESTORAGE_TEST_S3_ENDPOINT="http://127.0.0.1:59001"
export NESTORAGE_TEST_S3_ACCESS_KEY_ID="nestorage"
export NESTORAGE_TEST_S3_SECRET_ACCESS_KEY="nestorage-dev-minio"
make test-gated
```

`./internal/media/...` is already in `GATED_TEST_PACKAGES`
([`Makefile`](../Makefile), added by NSTR-34), so a `make test-gated` run
with both `NESTORAGE_TEST_DATABASE_URL` and `NESTORAGE_TEST_S3_ENDPOINT` set
exercises the database- and S3-gated suites together in one pass; either one
left unset self-skips only its own tests (the DB-gated ones still need
`NESTORAGE_TEST_DATABASE_URL` regardless — `test-gated`'s own guard clause
requires it).

Each test creates and tears down its own uniquely-named bucket against the
configured endpoint — no shared bucket to reset between runs, unlike the
database-gated suite's per-package database. `TestS3PhotoStore_Conformance`
reruns the identical port-level suite `TestLocalPhotoStore_Conformance`
(`photo_store_local_test.go`) runs against `LocalPhotoStore` — see
`conformance_test.go`'s own doc.

## CI runs both gated suites

The `test-gated` job in [`.github/workflows/ci.yml`](../.github/workflows/ci.yml)
runs `make test-gated` against its own dedicated `postgres:17-alpine` service
container — version 17 to match production, not the shared dev "nest"
instance this job never touches — with its own `NESTORAGE_TEST_DATABASE_URL`
pointing at a `nestorage_test` database, its `options` parameter carrying
`search_path=nestorage,public` the same way the local recipe above does. The
service's health check gates the job's steps, so there is no hand-rolled
ready-loop the way there is for a locally started container. The Makefile
target runs with `-v` so the job's log shows each gated test PASS or SKIP by
name — a package-level "ok" line alone can't distinguish "ran and passed"
from "every test skipped itself" (e.g. a misnamed or missing environment
variable).

MinIO runs alongside it as a plain `docker run -d` step, not a `services:`
entry: GitHub Actions' service-container support has no way to pass the
`server /data` command MinIO's image requires, and a `services:` container
always starts before any step runs, whereas `docker run -d` can be sequenced
and its readiness polled explicitly. The step exports
`NESTORAGE_TEST_S3_ENDPOINT` and the container's own test credentials before
`make test-gated` runs, so the S3-gated suite executes in the very same
`go test` invocation as the database-gated one.

## Moving existing data into the shared database (NSTR-119)

A one-time recipe for a database that predates the shared `nest` database
(NSTR-112) — Nestorage's tables sit directly in `public` of their own private
`nestorage` database. Run this once per environment (dev, then wherever
Nestorage is deployed) before pointing `DATABASE_URL` at the shared database.
Mirrors the equivalent Nestova recipe (`docs/deployment.md` in the nestova
repo, NSTR-118) — same shape, Nestorage's own schema name and extensions.

Back up the OLD database before any of this — the rename below is run
against the live database, not a copy:

```sh
pg_dump --format=custom "$OLD_DATABASE_URL" > old-database-pre-rename.dump
```

In the OLD database, move `public` (Nestorage's tables, plus the `pgcrypto`,
`citext`, `pg_trgm`, and `btree_gin` extension objects the rename drags
along) into a `nestorage` schema, then restore a fresh empty `public` and
return the extensions to it — later `citext` column, `gen_random_uuid()`,
and trigram/GIN index references resolve through `search_path`'s trailing
`public` entry, not a schema-qualified name, so they must live there and not
in `nestorage`. `--single-transaction` rolls back every statement below if
any one of them fails, rather than leaving the rename half-applied:

```sh
psql --set=ON_ERROR_STOP=1 --single-transaction "$OLD_DATABASE_URL" <<'SQL'
ALTER SCHEMA public RENAME TO nestorage;
CREATE SCHEMA public;
ALTER EXTENSION citext SET SCHEMA public;
ALTER EXTENSION pgcrypto SET SCHEMA public;
ALTER EXTENSION pg_trgm SET SCHEMA public;
ALTER EXTENSION btree_gin SET SCHEMA public;
SQL
```

The rename carries `public.goose_db_version` along as
`nestorage.goose_db_version` automatically — no separate step moves goose's
own bookkeeping.

Dump just the renamed schema and restore it into the shared database. The
four extensions must already be installed in the shared database's `public`
schema — Nestova's own consolidation (NSTR-118) already requires them there,
which is why that ticket blocks this one. Do **not** run `make migrate-up`
against the shared database before this restore: it would create the same
`nestorage` schema and empty tables the restore is about to create itself,
and the two collide. `--single-transaction` again makes the restore all-or-
nothing:

```sh
pg_dump --schema=nestorage --format=custom "$OLD_DATABASE_URL" > nestorage.dump
pg_restore --single-transaction --dbname="$SHARED_DATABASE_URL" nestorage.dump
```

Verify nothing was dropped before cutting `DATABASE_URL` over, comparing row
counts per table (and `goose_db_version`'s row count, proving its migration
history moved too) between `information_schema.tables` on the old database
and the same query against the `nestorage` schema of the new one. Only
after this restore is verified, run `make migrate-up` against
`DATABASE_URL` (with the `nestorage,public` search path) to apply any
migration added after the dump was taken but before cutover.

## No separate Nestorage backup timer

This repo ships no systemd unit or backup docs of its own — the appliance's
backup story lives entirely in Nestova's `docs/aws-backups.md` (nestova
repo): once both apps' tables and identity share one `nest` database, a
single `pg_dump` against it covers all three schemas. Do not add a second
backup timer here; there is nothing Nestorage-specific left to back up
separately.
