// Package config composes Nestorage's root configuration from nestcore's
// generic sub-configs. Configuration is read exclusively from environment
// variables so secrets are never committed; an optional .env file is
// honored in development only, via nestcore's LoadDotenv. Load fails fast,
// reporting every problem found in one pass rather than one at a time.
//
// This package holds only the sub-configs that have a consumer today
// (Server, DB, TLS, HSTS) plus Env. Config is the extension point a later
// sprint grows: add one field, one loader line, and one validate line here
// without touching what already exists (OCP) — see the ticket history for
// which nestcore loaders (Session, Crypto, S3, Email, Cache) and
// Nestorage-specific sections have no reader yet.
package config

import (
	"errors"
	"fmt"

	corecfg "github.com/ericfisherdev/nestcore/config"
)

// devDSN is the DATABASE_URL fallback applied only in dev when the
// environment does not supply one, matching compose.yaml's local Postgres
// service: host port 5433, not Postgres's default 5432 — Nestova's own
// compose service already binds 127.0.0.1:5432 on this host.
const devDSN = "postgres://nestorage:nestorage@localhost:5433/nestorage?sslmode=disable"

// Config holds Nestorage's validated runtime configuration, composed from
// nestcore's generic sub-configs plus the deployment environment.
type Config struct {
	Server corecfg.ServerConfig
	DB     corecfg.DBConfig
	TLS    corecfg.TLSConfig
	HSTS   corecfg.HSTSConfig
	// Env is the deployment environment: one of corecfg.EnvDev, EnvTest, or
	// EnvProd.
	Env string
}

// Load reads configuration from the environment and validates it. In
// development it first loads an optional .env file (godotenv never
// overwrites a variable that is already set, so the real environment always
// wins); env is re-read afterward since .env may itself set APP_ENV, and
// without the re-read every other field would pick up .env values while Env
// did not. It returns an aggregated error naming every missing or invalid
// variable, so an operator can fix them all in one pass.
func Load() (Config, error) {
	env := corecfg.AppEnv()

	var errs []error
	if env == corecfg.EnvDev {
		errs = append(errs, corecfg.LoadDotenv()...)
		env = corecfg.AppEnv()
	}

	server, serverErrs := corecfg.LoadServer()
	errs = append(errs, serverErrs...)
	db, dbErrs := corecfg.LoadDB()
	errs = append(errs, dbErrs...)
	hsts, hstsErrs := corecfg.LoadHSTS()
	errs = append(errs, hstsErrs...)
	tls := corecfg.LoadTLS()

	// Dev-only convenience DSN, applied before validation so the dev happy
	// path boots with no environment at all. Test and prod are left alone
	// so a missing DATABASE_URL still fails validation there, rather than
	// silently connecting to Nestorage's dev database.
	if env == corecfg.EnvDev && db.DSN == "" {
		db.DSN = devDSN
	}

	errs = append(errs, corecfg.ValidateAppEnv(env)...)
	errs = append(errs, server.Validate()...)
	errs = append(errs, db.Validate()...)
	errs = append(errs, hsts.Validate()...)
	errs = append(errs, tls.Validate()...)

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration:\n%w", errors.Join(errs...))
	}

	return Config{
		Server: server,
		DB:     db,
		TLS:    tls,
		HSTS:   hsts,
		Env:    env,
	}, nil
}
