package adapter

import (
	"context"
	"fmt"

	"github.com/ericfisherdev/nestorage/internal/storage/app"
)

// WithinBinTx runs fn inside one transaction, passing it an app.BinTxStores
// bundle — a BinRepository, an ItemRepository (for the moved-event
// fan-out's ListIDsByBin), and an ItemEventRepository, all bound to that
// transaction — so fn's GetForUpdate/Move/ListIDsByBin/Append calls share
// the same row lock and either all commit or all roll back (NSTR-41: the
// location swap and every fanned-out item_event row must never disagree) —
// NSTR-30's bin-move sibling of WithinTx (operations_uow.go). It reuses
// PostgresUnitOfWork's existing pool via a second method rather than
// introducing a second unit-of-work type, mirroring WithinTx's own
// begin/defer-rollback/commit shape exactly.
func (u *PostgresUnitOfWork) WithinBinTx(ctx context.Context, fn func(app.BinTxStores) error) error {
	tx, err := u.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("storage/adapter: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	stores := app.BinTxStores{
		Bins:   NewBinRepository(tx),
		Items:  NewItemRepository(tx),
		Events: NewItemEventRepository(tx),
	}
	if err := fn(stores); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("storage/adapter: commit transaction: %w", err)
	}
	return nil
}
