package adapter

import (
	"context"
	"fmt"

	"github.com/ericfisherdev/nestorage/internal/storage/app"
)

// WithinReturnRequestTx runs fn inside one transaction, passing it an
// app.ReturnRequestTxStores bundle — a ReturnRequestRepository and an
// ItemEventRepository, both bound to that transaction — so fn's
// Create/Cancel/Append calls share the same transaction and either all
// commit or all roll back (NSTR-43's reconciliation R7: a return_request
// row and the item_event row that records it must never disagree). The
// return-request analog of WithinTx/WithinItemTx (operations_uow.go/
// item_uow.go); it reuses PostgresUnitOfWork's existing pool via a fourth
// method rather than introducing a fourth unit-of-work type, mirroring
// their own begin/defer-rollback/commit shape exactly.
func (u *PostgresUnitOfWork) WithinReturnRequestTx(ctx context.Context, fn func(app.ReturnRequestTxStores) error) error {
	tx, err := u.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("storage/adapter: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	stores := app.ReturnRequestTxStores{
		ReturnRequests: NewReturnRequestRepository(tx),
		Events:         NewItemEventRepository(tx),
	}
	if err := fn(stores); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("storage/adapter: commit transaction: %w", err)
	}
	return nil
}
