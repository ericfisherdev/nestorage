package adapter

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ericfisherdev/nestorage/internal/storage/app"
)

// PostgresUnitOfWork is the pgx-backed unit of work app.OperationService's
// add/remove/return operations run inside: one transaction per operation,
// with the item row locked FOR UPDATE for the transaction's duration (see
// ItemRepository.GetForUpdate's own doc) so a concurrent operation against
// the same item blocks on the lock rather than racing it.
type PostgresUnitOfWork struct {
	pool *pgxpool.Pool
}

// NewPostgresUnitOfWork constructs the unit of work over the shared pool.
// Panics on a nil pool, matching every other constructor in this package.
func NewPostgresUnitOfWork(pool *pgxpool.Pool) *PostgresUnitOfWork {
	if pool == nil {
		panic("storage/adapter: NewPostgresUnitOfWork requires a non-nil pool")
	}
	return &PostgresUnitOfWork{pool: pool}
}

// WithinTx runs fn inside one transaction, passing it an ItemRepository
// bound to that transaction so fn's GetForUpdate/Move calls share the same
// row lock and either both commit or both roll back. Mirrors
// identity/adapter.Provisioner.CreateFirstAdmin's begin/defer-rollback/
// commit shape: the deferred Rollback is a no-op once Commit has already
// succeeded, and otherwise undoes anything fn wrote before returning its
// error.
func (u *PostgresUnitOfWork) WithinTx(ctx context.Context, fn func(app.ItemStore) error) error {
	tx, err := u.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("storage/adapter: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(NewItemRepository(tx)); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("storage/adapter: commit transaction: %w", err)
	}
	return nil
}
