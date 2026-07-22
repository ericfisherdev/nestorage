package adapter

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// firstAdminAdvisoryLock is a fixed key for the transaction-scoped advisory
// lock that serializes first-run admin provisioning across connections —
// bytes spell "NSTR_ADM". Postgres's advisory lock functions take a signed
// bigint; this value's top bit is 0, so it fits int64 without wrapping.
const firstAdminAdvisoryLock int64 = 0x4E5354525F41444D

// FirstAdminProvisioner is the outbound port OnboardingHandlers depends on
// for creating the first admin user. Satisfied by *Provisioner; the
// indirection lets handler tests substitute a fake without a database.
type FirstAdminProvisioner interface {
	CreateFirstAdmin(ctx context.Context, u *domain.User) error
}

// Provisioner performs first-run admin creation as one atomic, race-proof
// operation: a transaction-scoped advisory lock makes the check-then-insert
// atomic across connections, so concurrent wizard submissions cannot create
// two admins.
type Provisioner struct {
	pool *pgxpool.Pool
}

// Compile-time assurance the adapter satisfies the port.
var _ FirstAdminProvisioner = (*Provisioner)(nil)

// NewProvisioner constructs a Provisioner over the shared pool. Panics on a
// nil pool — a misconfigured composition root should fail at startup, not
// at the first wizard submission.
func NewProvisioner(pool *pgxpool.Pool) *Provisioner {
	if pool == nil {
		panic("identity/adapter: NewProvisioner requires a non-nil pool")
	}
	return &Provisioner{pool: pool}
}

// CreateFirstAdmin creates u as the first admin user inside a transaction
// serialized by a fixed-key advisory lock, so a concurrent submission
// cannot race the "no user yet" check with the insert. Returns
// domain.ErrSetupComplete when a user already exists — the lost-race case,
// which the caller treats as "setup is already done", not a failure.
func (p *Provisioner) CreateFirstAdmin(ctx context.Context, u *domain.User) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("provision first admin: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", firstAdminAdvisoryLock); err != nil {
		return fmt.Errorf("provision first admin: acquire advisory lock: %w", err)
	}

	repo := NewUserRepository(tx)
	has, err := repo.HasAnyUser(ctx)
	if err != nil {
		return fmt.Errorf("provision first admin: check existing users: %w", err)
	}
	if has {
		return domain.ErrSetupComplete
	}
	if err := repo.Create(ctx, u); err != nil {
		return fmt.Errorf("provision first admin: create user: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("provision first admin: commit transaction: %w", err)
	}
	return nil
}
