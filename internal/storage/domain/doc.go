// Package domain holds the storage bounded context's domain model: the
// Location aggregate (NSTR-26), typed ids, sentinel errors, the
// LocationRepository port, and the ValidateLocationName rule every write
// path shares. NSTR-27 (bins) and NSTR-28 (items) add their own aggregates
// to this same package rather than splitting into separate contexts.
// NSTR-29 adds Item's placement transition methods (EnterBin/CheckOut/
// ReturnTo) alongside Placement rather than a new file, since they are
// Item-shaped rules over the same placement model. Persistence adapters
// live in the sibling adapter package; the application service that
// orchestrates these transitions transactionally lives in the sibling app
// package.
package domain
