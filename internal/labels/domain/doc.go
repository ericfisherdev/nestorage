// Package domain holds the labels bounded context's domain model (NSTR-47):
// LabelSize and its Registry, the BinLabel/Document rendering values, and
// the LabelRenderer port. It is a fresh bounded context, not an extension of
// storage or media — pure geometry plus the rendering seam, so the
// thermal-printer path can be added later purely by adding an adapter. No
// PDF-specific type appears anywhere in this package; gopdf (or any other
// rendering library) is an adapter-package concern, first introduced by
// NSTR-49's PDF adapter.
//
// LabelRenderer's implementation and the batch service that drives it live
// in later tickets: NSTR-49 adds the gopdf-backed adapter (internal/labels/
// adapter) and NSTR-50 adds the app-layer batch service (internal/labels/
// app) that maps visible bins into BinLabel values and enforces its own
// print-batch cap. This ticket's registry is what NSTR-51's size-selection
// UI reads via Registry.List.
package domain
