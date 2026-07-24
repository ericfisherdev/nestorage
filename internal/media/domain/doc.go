// Package domain holds the media bounded context's domain model (NSTR-34):
// the Photo aggregate, typed ids, sentinel errors, the PhotoStore/
// PhotoValidator/PhotoRepository ports, and StorageBackend. It is a fresh
// bounded context, not an extension of storage or identity — it reimplements
// Nestova's internal/media package against Nestorage's own identity model
// (Nestova's media package is reference only; it is keyed by household
// identity and was measured as not extractable to nestcore). NSTR-84 adds
// the PhotoThumbnailer port, PhotoVariant, and Photo.ThumbnailRef.
//
// Every photo belongs to exactly one storage item (internal/storage), keyed
// by storagedomain.ItemID: dedup, the storage key layout, and visibility are
// all item-scoped rather than household-scoped, which is what dissolves the
// refcounting problem Nestova's cross-household PhotoStore had to solve (see
// PhotoRepository's own doc). Persistence and storage adapters live in the
// sibling adapter package; PhotoService, the application service that
// orchestrates upload/read/delete, lives in the sibling app package.
package domain
