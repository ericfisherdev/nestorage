package adapter

import (
	"context"
	"fmt"

	"github.com/ericfisherdev/nestorage/internal/storage/app"
)

// WithinBinTx runs fn inside one transaction, passing it a BinRepository
// bound to that transaction so fn's GetForUpdate/Move calls share the same
// row lock and either both commit or both roll back — NSTR-30's bin-move
// sibling of WithinTx (operations_uow.go). It reuses PostgresUnitOfWork's
// existing pool via a second method rather than introducing a second
// unit-of-work type, mirroring WithinTx's own begin/defer-rollback/commit
// shape exactly.
func (u *PostgresUnitOfWork) WithinBinTx(ctx context.Context, fn func(app.BinStore) error) error {
	tx, err := u.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("storage/adapter: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(NewBinRepository(tx)); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("storage/adapter: commit transaction: %w", err)
	}
	return nil
}
