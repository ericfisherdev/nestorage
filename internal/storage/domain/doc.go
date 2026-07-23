// Package domain holds the storage bounded context's domain model: the
// Location aggregate (NSTR-26), typed ids, sentinel errors, the
// LocationRepository port, and the ValidateLocationName rule every write
// path shares. NSTR-27 (bins) and NSTR-28 (items) add their own aggregates
// to this same package rather than splitting into separate contexts.
// Persistence adapters live in the sibling adapter package.
package domain
