package adapter

import (
	"context"
	"fmt"

	"github.com/ericfisherdev/nestorage/internal/storage/app"
)

// WithinItemTx runs fn inside one transaction, passing it an
// app.ItemTxStores bundle — an ItemRepository and an ItemEventRepository,
// both bound to that transaction — so fn's Create/GetForUpdate/Update/
// Delete/Append calls share the same row lock and either all commit or all
// roll back (NSTR-41: an item's create/edit/delete and the item_event row
// that records it must never disagree) — the item-lifecycle sibling of
// WithinTx/WithinBinTx (operations_uow.go/mover_uow.go). It reuses
// PostgresUnitOfWork's existing pool via a third method rather than
// introducing a third unit-of-work type, mirroring their own
// begin/defer-rollback/commit shape exactly.
func (u *PostgresUnitOfWork) WithinItemTx(ctx context.Context, fn func(app.ItemTxStores) error) error {
	tx, err := u.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("storage/adapter: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	stores := app.ItemTxStores{
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
