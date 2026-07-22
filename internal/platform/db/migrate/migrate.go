// Package migrate owns Nestorage's embedded migration set and wires it into
// nestcore's migration runner. Applying, rolling back, and inspecting
// migrations is entirely nestcore/db/migrate's job (github.com/ericfisherdev/nestcore,
// tagged v0.1.0) — nothing about goose is re-implemented here. This package
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

// New returns a Runner over Nestorage's embedded migration set.
func New() (*ncmigrate.Runner, error) {
	return ncmigrate.New(migrationsFS, "migrations")
}
