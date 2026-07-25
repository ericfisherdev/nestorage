// Package config composes Nestorage's root configuration from nestcore's
// generic sub-configs. Configuration is read exclusively from environment
// variables so secrets are never committed; an optional .env file is
// honored in development only, via nestcore's LoadDotenv. Load fails fast,
// reporting every problem found in one pass rather than one at a time.
//
// This package holds only the sub-configs that have a consumer today
// (Server, DB, TLS, HSTS, Session) plus Env. Config is the extension point a
// later sprint grows: add one field, one loader line, and one validate line
// here without touching what already exists (OCP) — see the ticket history
// for which nestcore loaders (Crypto, S3, Email, Cache) and
// Nestorage-specific sections have no reader yet.
package config

import (
	"errors"
	"fmt"

	corecfg "github.com/ericfisherdev/nestcore/config"
)

// Config holds Nestorage's validated runtime configuration, composed from
// nestcore's generic sub-configs plus the deployment environment.
type Config struct {
	Server  corecfg.ServerConfig
	DB      corecfg.DBConfig
	TLS     corecfg.TLSConfig
	HSTS    corecfg.HSTSConfig
	Session corecfg.SessionConfig
	// Media configures photo storage (NSTR-34) — Nestorage-local, since
	// nestcore has no media loader.
	Media MediaConfig
	// Email configures the opt-in email notification channel (NSTR-89) —
	// Nestorage-local, since nestcore's own EmailConfig does not fit this
	// ticket's env var contract (see email.go's own doc).
	Email EmailConfig
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
	// LoadSession takes env to resolve SESSION_COOKIE_SECURE's auto setting
	// (Secure only in EnvProd), so it must run after env is fully resolved
	// (post dotenv re-read) above.
	session, sessionErrs := corecfg.LoadSession(env)
	errs = append(errs, sessionErrs...)
	media, mediaErrs := LoadMedia()
	errs = append(errs, mediaErrs...)
	email, emailErrs := LoadEmail()
	errs = append(errs, emailErrs...)

	errs = append(errs, corecfg.ValidateAppEnv(env)...)
	errs = append(errs, server.Validate()...)
	errs = append(errs, db.Validate()...)
	errs = append(errs, hsts.Validate()...)
	errs = append(errs, tls.Validate()...)
	errs = append(errs, session.Validate(env)...)
	errs = append(errs, media.Validate()...)
	errs = append(errs, email.Validate()...)

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration:\n%w", errors.Join(errs...))
	}

	return Config{
		Server:  server,
		DB:      db,
		TLS:     tls,
		HSTS:    hsts,
		Session: session,
		Media:   media,
		Email:   email,
		Env:     env,
	}, nil
}
