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

The `postgres` service in [`compose.yaml`](../compose.yaml) is the easiest
way to get one:

```sh
docker compose up -d
export NESTORAGE_TEST_DATABASE_URL="postgres://nestorage:nestorage@127.0.0.1:5433/nestorage_test?sslmode=disable"
make test-gated
```

`nestorage_test` is created automatically the first time the compose
volume is created, by [`compose/initdb/01-test-database.sql`](../compose/initdb/01-test-database.sql)
— see that file for why an existing `nestorage-pgdata` volume from before it
existed needs the database created by hand, once.

`make test-gated` names the gated packages explicitly
(`GATED_TEST_PACKAGES` in the [`Makefile`](../Makefile)). `go test ./...`
with the variable set works too and runs everything; the explicit target
exists so a gated run is deliberate and its package list is reviewable.

### Prerequisites

- **A Postgres reachable at that DSN, version 17.** Production runs 17, and
  `compose.yaml` pins the same major so the gated suite exercises what the
  appliance actually runs.
- **A database named `test` or ending in `_test`.** Enforced as a safety
  rail: the harness refuses to run otherwise, because it drops and
  recreates schemas. `nestorage_test` is the convention.
- **The `CREATEDB` privilege on that role.** The harness creates a database
  per package on demand. The compose service's `nestorage` role already has
  it (the official postgres image grants `POSTGRES_USER` superuser); a
  purpose-made role needs it granted:

  ```sql
  ALTER ROLE nestorage_test CREATEDB;
  ```

  Without it, gated tests fail with a `create database` error naming this
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
both setup and cleanup), so repeat runs are fast. Drop them wholesale by
dropping the compose volume, or:

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
runs `make test-gated` against a `postgres:17-alpine` service container,
matching `compose.yaml`'s major version, with its own `NESTORAGE_TEST_DATABASE_URL`
pointing at a `nestorage_test` database. The service's health check gates
the job's steps, so there is no hand-rolled ready-loop the way there is for
a locally started container. The Makefile target runs with `-v` so the
job's log shows each gated test PASS or SKIP by name — a package-level "ok"
line alone can't distinguish "ran and passed" from "every test skipped
itself" (e.g. a misnamed or missing environment variable).

MinIO runs alongside it as a plain `docker run -d` step, not a `services:`
entry: GitHub Actions' service-container support has no way to pass the
`server /data` command MinIO's image requires, and a `services:` container
always starts before any step runs, whereas `docker run -d` can be sequenced
and its readiness polled explicitly. The step exports
`NESTORAGE_TEST_S3_ENDPOINT` and the container's own test credentials before
`make test-gated` runs, so the S3-gated suite executes in the very same
`go test` invocation as the database-gated one.
