// Package app contains the media context's application service.
// PhotoService (photo_service.go) orchestrates the full upload/read/delete
// pipeline (Sprint 5 reconciliation R1/R4): stage, validate, hash, key, Put,
// dedup, persist, attach — plus the visibility-scoped reads/mutations
// (ListForItem, Open, DirectURL, Delete, SetPrimary, Reorder) every route
// NSTR-37 will expose over item_photo's ordering. It depends only on
// domain.PhotoStore/PhotoValidator/PhotoRepository plus a narrow itemGetter
// port, never on any infrastructure package directly. NSTR-84 extends the
// pipeline with a PhotoThumbnailer-backed thumbnail generation step and a
// PhotoVariant parameter on Open/DirectURL.
package app
