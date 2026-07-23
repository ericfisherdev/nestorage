// Package adapter contains the storage context's outbound adapters: the
// Postgres implementation of domain.LocationRepository (NSTR-26). NSTR-27
// (bins) and NSTR-28 (items) add their own repositories to this same
// package. NSTR-29 adds PostgresUnitOfWork (operations_uow.go), the
// transactional seam app.OperationService's add/remove/return operations
// run inside, built over ItemRepository rather than a new repository type.
// NSTR-30 adds a second method to that same PostgresUnitOfWork,
// WithinBinTx (mover_uow.go), the transactional seam app.BinMover.Move runs
// inside, built over BinRepository the same way.
package adapter
