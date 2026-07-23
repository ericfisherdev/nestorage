// Package adapter contains the storage context's outbound adapters: the
// Postgres implementation of domain.LocationRepository (NSTR-26). NSTR-27
// (bins) and NSTR-28 (items) add their own repositories to this same
// package. NSTR-29 adds PostgresUnitOfWork (operations_uow.go), the
// transactional seam app.OperationService's add/remove/return operations
// run inside, built over ItemRepository rather than a new repository type.
// NSTR-30 adds a second method to that same PostgresUnitOfWork,
// WithinBinTx (mover_uow.go), the transactional seam app.BinMover.Move runs
// inside, built over BinRepository the same way. NSTR-31 adds this
// context's inbound web adapters — BinsWebHandlers (bins_web.go) and
// LocationsWebHandlers (locations_web.go) — mirroring identity/adapter's
// own web-handler shape (web_common.go holds what both share).
package adapter
