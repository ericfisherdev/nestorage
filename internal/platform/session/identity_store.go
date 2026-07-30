package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultCleanupInterval mirrors pgxstore's own default (see its README):
// how often expired rows are purged so identity.sessions does not grow
// unboundedly.
const defaultCleanupInterval = 5 * time.Minute

// identityStore is an scs.Store (and scs.CtxStore/IterableCtxStore) backed
// by identity.sessions rather than an app-schema `sessions` table.
//
// Interim note (NSTR-116): this exists only because nestcore's identity Go
// package (NSTR-121) does not ship a session store yet. github.com/alexedwards/scs/pgxstore
// hardcodes the unqualified table name `sessions`, so it cannot target a
// schema-qualified `identity.sessions` without every unqualified query
// elsewhere in the app also resolving against `identity` via search_path —
// a pool-wide change out of proportion to this ticket. This type mirrors
// pgxstore's own schema and queries exactly, scoped to the one table it
// needs. Replace with nestcore's own store once NSTR-121 ships one.
type identityStore struct {
	pool   *pgxpool.Pool
	cancel context.CancelFunc
	logger *slog.Logger
}

// Compile-time assurance the store satisfies the optional scs interfaces the
// app relies on beyond scs.Store, which is all sm.Store's declared type
// checks: CtxStore for the request-scoped calls, and IterableCtxStore for
// SessionRevoker.RevokeAll's sm.Iterate — scs panics rather than degrading
// when the latter is missing.
var (
	_ scs.CtxStore         = (*identityStore)(nil)
	_ scs.IterableCtxStore = (*identityStore)(nil)
)

// newIdentityStore constructs the store and starts its background cleanup
// goroutine at defaultCleanupInterval, mirroring pgxstore.New. Callers that
// need to stop it (tests) get the token back from StopCleanup. Panics on a
// nil logger, matching every other constructor in this codebase.
func newIdentityStore(pool *pgxpool.Pool, logger *slog.Logger) *identityStore {
	if logger == nil {
		panic("platform/session: newIdentityStore requires a non-nil logger")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &identityStore{pool: pool, cancel: cancel, logger: logger}
	go s.startCleanup(ctx, defaultCleanupInterval)
	return s
}

// StopCleanup stops the background cleanup goroutine, mirroring
// pgxstore.PostgresStore.StopCleanup — used by tests so a short-lived
// *testing.T does not leak a running goroutine.
func (s *identityStore) StopCleanup() {
	s.cancel()
}

func (s *identityStore) startCleanup(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := s.pool.Exec(ctx, `DELETE FROM identity.sessions WHERE expiry < now()`); err != nil {
				s.logger.ErrorContext(ctx, "identity session store: purge expired sessions", "error", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

// FindCtx implements scs.CtxStore.
func (s *identityStore) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM identity.sessions WHERE token = $1 AND expiry > now()`, token).Scan(&data)
	switch {
	case err == nil:
		return data, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("identity session store: find: %w", err)
	}
}

// CommitCtx implements scs.CtxStore.
func (s *identityStore) CommitCtx(ctx context.Context, token string, data []byte, expiry time.Time) error {
	const q = `
		INSERT INTO identity.sessions (token, data, expiry)
		VALUES ($1, $2, $3)
		ON CONFLICT (token) DO UPDATE SET data = EXCLUDED.data, expiry = EXCLUDED.expiry`
	if _, err := s.pool.Exec(ctx, q, token, data, expiry); err != nil {
		return fmt.Errorf("identity session store: commit: %w", err)
	}
	return nil
}

// DeleteCtx implements scs.CtxStore.
func (s *identityStore) DeleteCtx(ctx context.Context, token string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM identity.sessions WHERE token = $1`, token); err != nil {
		return fmt.Errorf("identity session store: delete: %w", err)
	}
	return nil
}

// AllCtx implements scs.IterableCtxStore — SessionRevoker.RevokeAll depends
// on it via sm.Iterate (see session_revoker.go).
func (s *identityStore) AllCtx(ctx context.Context) (map[string][]byte, error) {
	rows, err := s.pool.Query(ctx, `SELECT token, data FROM identity.sessions WHERE expiry > now()`)
	if err != nil {
		return nil, fmt.Errorf("identity session store: all: %w", err)
	}
	defer rows.Close()

	sessions := make(map[string][]byte)
	for rows.Next() {
		var token string
		var data []byte
		if err := rows.Scan(&token, &data); err != nil {
			return nil, fmt.Errorf("identity session store: all: scan: %w", err)
		}
		sessions[token] = data
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("identity session store: all: %w", err)
	}
	return sessions, nil
}

// Find, Commit, and Delete implement the non-context scs.Store interface,
// which scs.SessionManager falls back to when a caller does not go through
// the *Ctx methods. Delegating to the Ctx methods with context.Background()
// mirrors pgxstore's own non-ctx methods.
func (s *identityStore) Find(token string) ([]byte, bool, error) {
	return s.FindCtx(context.Background(), token)
}

func (s *identityStore) Commit(token string, data []byte, expiry time.Time) error {
	return s.CommitCtx(context.Background(), token, data, expiry)
}

func (s *identityStore) Delete(token string) error {
	return s.DeleteCtx(context.Background(), token)
}
