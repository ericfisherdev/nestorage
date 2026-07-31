// Package migrate owns Nestorage's embedded migration set and wires it into
// nestcore's migration runner. Applying, rolling back, and inspecting
// migrations is entirely nestcore/db/migrate's job (github.com/ericfisherdev/nestcore,
// tagged v0.2.0) — nothing about goose is re-implemented here. This package
// only supplies the embedded filesystem those migrations live in and the
// on-disk directory new ones are scaffolded into.
package migrate

import (
	"embed"

	ncmigrate "github.com/ericfisherdev/nestcore/db/migrate"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// SourceDir is the on-disk migrations directory relative to the repo root,
// where `go run ./cmd/migrate create` writes new migration files. Run that
// subcommand from the repo root.
const SourceDir = "internal/platform/db/migrate/migrations"

// Schema is the dedicated Postgres schema Nestorage's own tables and goose's
// bookkeeping live in, inside the "nest" database shared with Nestova and the
// identity schema (NSTR-119). Exported so cmd/server's boot guard can check
// current_schema() against the same literal this package migrates into,
// without a second, possibly-drifting copy of the name.
const Schema = "nestorage"

// versionTable is goose's schema-qualified bookkeeping table name.
const versionTable = Schema + ".goose_db_version"

// New returns a Runner over Nestorage's embedded migration set. WithEnsureSchema
// creates the nestorage schema before any goose command runs — including the
// very first Up against a fresh database — since goose itself never creates
// a schema and would otherwise fail trying to create versionTable inside one
// that does not exist yet. WithVersionTable moves goose's own bookkeeping
// into that schema too, so it coexists with Nestova's and identity's own
// version tables in the same shared database.
func New() (*ncmigrate.Runner, error) {
	return ncmigrate.New(migrationsFS, "migrations",
		ncmigrate.WithVersionTable(versionTable),
		ncmigrate.WithEnsureSchema(Schema),
	)
}
