package domain

// PhotoRef is the narrow reference the storage context's list-view builders
// carry across the storage/media context boundary for a single item's
// primary photo (Sprint 5 reconciliation R8): just enough to construct a
// thumbnail URL, never media's own domain.Photo or domain.StorageRef
// reaching into this package.
//
// Declared here, in storage's own domain package, rather than storage
// importing media's: the established dependency direction between the two
// contexts runs from media into storage (see
// internal/media/app.itemGetter's own doc — PhotoService already depends on
// this package's ItemID/Item), never the other way. This type is what
// internal/media/app.PhotoService.ListPrimaryThumbRefs returns to satisfy
// internal/storage/adapter's own unexported primaryPhotoRefLister port,
// keeping media's own domain.PhotoID out of this package entirely — the
// same "narrow port, narrow type, no cross-context table join" arrangement
// R8 binds for the read itself.
type PhotoRef struct {
	// PhotoID is the primary photo's id, string-typed rather than media's
	// own domain.PhotoID — storage's domain package never imports media's.
	// The zero value (empty string) means "no primary photo", the contract
	// every list-view builder's thumbnail-or-placeholder branch relies on.
	PhotoID string
}
