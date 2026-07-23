// Command server is the Nestorage HTTP server entrypoint. It composes
// configuration, the database pool, and nestcore's HTTP server into a
// single process with a graceful shutdown lifecycle. Application routes are
// registered through shellHandlers (shell.go), plugged into
// httpserver.Deps.Routes — the seam NSTR-15 left for this ticket to fill.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	corecfg "github.com/ericfisherdev/nestcore/config"
	"github.com/ericfisherdev/nestcore/crypto"
	"github.com/ericfisherdev/nestcore/db"
	"github.com/ericfisherdev/nestcore/httpserver"
	"github.com/ericfisherdev/nestcore/httpserver/middleware"
	"github.com/ericfisherdev/nestcore/metrics"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identityapp "github.com/ericfisherdev/nestorage/internal/identity/app"
	"github.com/ericfisherdev/nestorage/internal/platform/config"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
)

// shutdownTimeout bounds how long in-flight requests have to drain on
// shutdown.
const shutdownTimeout = 15 * time.Second

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server exited with error", "error", err)
		os.Exit(1)
	}
}

// run listens for SIGINT/SIGTERM and delegates the rest of the lifecycle to
// serve. It is split from serve so a test can drive serve directly, under
// its own cancellable context, instead of sending a real OS signal to the
// test process — which would tear down the whole `go test` run, not just
// the server under test.
func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, logger)
}

// serve loads configuration, wires the HTTP server, and blocks until ctx is
// cancelled — by run's signal handling, or directly by a caller — then
// drains in-flight requests via a graceful shutdown.
func serve(ctx context.Context, logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Establish the Postgres pool up front so a bad DSN or unreachable
	// database fails fast at boot (db.New bounds its own ping with
	// DB.ConnTimeout). context.Background(), not ctx: an interrupt racing
	// this initial check should not turn a real DSN/connectivity problem
	// into an ambiguous context-cancelled error.
	pool, err := db.New(context.Background(), cfg.DB)
	if err != nil {
		return err
	}
	defer pool.Close()
	logger.Info("connected to postgres", "max_conns", pool.Config().MaxConns)

	registry := metrics.NewRegistry()
	// "nestorage" namespaces every metric this process emits, so Nestova and
	// Nestorage can share a scrape target without their HTTP metrics colliding.
	httpMetrics := metrics.NewHTTPMetrics(registry, "nestorage")

	// Identity composition: the session manager (backed by the shared pool
	// via pgxstore), the user repository, the first-run provisioner, and
	// the onboarding wizard it backs. SetupGuard must be the outermost
	// feature middleware so an unconfigured app is sent to the wizard
	// before anything else — including session loading — runs; LoadAndSave
	// wraps the routes so the session is loaded and Set-Cookie written for
	// every request that gets past the guard. Authenticate runs after
	// LoadAndSave (it needs the session already loaded) and resolves the
	// signed-in user into the request context for the settings/devices
	// screen and shellNav to read back via identityadapter.CurrentUser.
	// NSTR-24's resolve (built further down, once its dependencies exist)
	// runs the same session lookup through the Principal model instead,
	// which is what RequireAdmin reads back via CurrentPrincipal.
	sm := session.New(pool, cfg.Session)
	identityRepo := identityadapter.NewUserRepository(pool)
	provisioner := identityadapter.NewProvisioner(pool)
	onboarding := identityadapter.NewOnboardingHandlers(identityRepo, provisioner, sm, logger)
	setupGuard := identityadapter.SetupGuard(identityRepo, logger)

	hasher := crypto.NewHasher(crypto.DefaultParams())
	authenticator := identityapp.NewAuthenticator(identityRepo, hasher)
	// loginLimiter is shared between the session-cookie login handler and
	// NSTR-22's device-token exchange endpoint below: both verify a
	// password against the same credential store, so an attacker locked
	// out of one must not get a fresh run of attempts against the other
	// (see LoginAttemptLimiter's own doc).
	loginLimiter := identityadapter.NewLoginAttemptLimiter()
	login := identityadapter.NewHandlers(sm, authenticator, loginLimiter, logger)
	authenticate := identityadapter.Authenticate(sm, identityRepo, logger)

	// NSTR-22's device tokens: DeviceTokenService implements
	// identityapp.CredentialRevoker directly (RevokeAll), so it is registered
	// into the Revokers slice below without any adapter-side wrapper type —
	// the OCP seam NSTR-21's own comment reserves this for. time.Now is
	// injected as the clock so LastUsedAt throttling and revocation
	// timestamps are real wall-clock time in production.
	deviceTokenRepo := identityadapter.NewDeviceTokenRepository(pool)
	deviceTokenService := identityapp.NewDeviceTokenService(deviceTokenRepo, identityRepo, authenticator, loginLimiter, time.Now, logger)
	deviceTokenAPI := identityadapter.NewDeviceTokenAPIHandlers(deviceTokenService, logger)
	deviceTokenWeb := identityadapter.NewDeviceTokenWebHandlers(deviceTokenService, sm, newDeviceSettingsLayout(), logger)

	// NSTR-23's account api key: the single credential the Nestova
	// integration authenticates with. NewAPIKeyService returns an error
	// rather than panicking on a nil dependency (see its own doc); every
	// dependency here is non-nil, so the error is checked but never
	// expected in practice.
	apiKeyRepo := identityadapter.NewAPIKeyRepository(pool)
	apiKeyService, err := identityapp.NewAPIKeyService(apiKeyRepo, time.Now, logger)
	if err != nil {
		return err
	}
	apiKeyWeb := identityadapter.NewAPIKeyWebHandlers(apiKeyService, sm, newAPIKeySettingsLayout(), logger)

	// NSTR-21's admin user management: Revokers is the open seam NSTR-22
	// plugs its device-token revoker into (OCP) — session revocation and
	// device-token revocation, so deactivating (or resetting the password
	// of) a user invalidates both.
	revokers := identityapp.Revokers{identityadapter.NewSessionRevoker(sm), deviceTokenService}
	adminService := identityapp.NewAdminService(identityRepo, hasher, revokers, logger)
	usersHandlers := identityadapter.NewUsersWebHandlers(adminService, sm, newAdminUsersLayout(), logger)

	// NSTR-24's principal resolution: one Chain dispatching a request's
	// credential — the session cookie, a device token, or the account api
	// key — to the Resolver that wraps it into a domain.Principal, and one
	// Denier every denial (Resolve, RequireAdmin) answers through so no
	// handler invents its own 401/403 shape.
	denier := identityadapter.NewDenier(logger)
	principals := identityadapter.NewChain(
		identityadapter.NewSessionResolver(sm, identityRepo, logger),
		identityadapter.NewDeviceTokenResolver(deviceTokenService),
		identityadapter.NewAPIKeyResolver(apiKeyService),
	)
	resolve := identityadapter.Resolve(principals, denier, logger)

	srv := httpserver.New(httpserver.Config{
		Server: cfg.Server,
		HSTS:   cfg.HSTS,
	}, httpserver.Deps{
		Logger: logger,
		Ready:  readiness(pool),
		// metrics.Handler keeps the promhttp dependency inside the metrics
		// package and reports scrape errors as metrics instead of failing
		// silently.
		MetricsHandler: metrics.Handler(registry),
		HTTPMetrics:    httpMetrics,
		Routes:         newAppRoutes(logger, onboarding, login, usersHandlers, deviceTokenAPI, deviceTokenWeb, apiKeyWeb, denier),
		// sm.LoadAndSave loads the session before authenticate (NSTR-20's
		// session-based CurrentUser, still consumed by settingsMux and
		// shellNav) and resolve (NSTR-24's Principal, consumed by
		// RequireAdmin) each read it.
		Middleware: []middleware.Middleware{setupGuard, sm.LoadAndSave, authenticate, resolve},
	})

	// Surface listen errors from the background goroutine to the main flow.
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("starting http server", "addr", cfg.Server.Addr, "env", cfg.Env, "tls", cfg.TLS.Enabled())
		if err := listenAndServe(srv, cfg.TLS); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	// A private cancel so either branch below — a server error or an
	// already-cancelled ctx — ends up with ctx definitively cancelled before
	// shutdown proceeds. There are no background workers to drain in this
	// ticket, but this keeps that guarantee in place for the ones a later
	// sprint adds against the same ctx.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var runErr error
	select {
	case err := <-serverErr:
		runErr = err
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}
	cancel()

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	shutdownErr := srv.Shutdown(shutdownCtx)

	if shutdownErr != nil {
		return errors.Join(runErr, shutdownErr)
	}
	return runErr
}

// listenAndServe starts srv with app-terminated TLS when cert+key are
// configured, otherwise plain HTTP. Both paths return http.ErrServerClosed
// on a graceful Shutdown, which the caller treats as a clean exit — the
// Tailscale deployment (tailscale serve terminates TLS) uses the plain-HTTP
// path. The branch is a thin wrapper so the cert-configured decision
// (TLSConfig.Enabled) stays unit-testable without binding a socket.
func listenAndServe(srv *http.Server, tlsCfg corecfg.TLSConfig) error {
	if tlsCfg.Enabled() {
		return srv.ListenAndServeTLS(tlsCfg.CertFile, tlsCfg.KeyFile)
	}
	return srv.ListenAndServe()
}

// readiness returns the ReadinessFunc backing the /readyz probe: ready when
// pool reports a live connection.
func readiness(pool *pgxpool.Pool) httpserver.ReadinessFunc {
	return func(ctx context.Context) error {
		return db.Health(ctx, pool)
	}
}
