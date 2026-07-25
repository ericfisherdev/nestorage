// Package adapter holds the labels bounded context's outbound adapters —
// implementations of the ports internal/labels/domain declares. NSTR-49
// adds PDFSheetRenderer (pdf_sheet.go), the first domain.LabelRenderer
// implementation: it lays BinLabel values out on a LabelSize's grid using
// github.com/signintech/gopdf (pure Go, no CGO, so the binary keeps
// cross-compiling to arm64) and returns a PDF domain.Document, entirely in
// memory — no temporary files, so it works with the binary running as a
// locked-down service user. go-pdf/fpdf was rejected: it is archived.
//
// The Hanken Grotesk TrueType program PDFSheetRenderer embeds
// (fonts/HankenGrotesk-Regular.ttf, OFL-licensed — see fonts/OFL.txt) is a
// new asset: the repo otherwise only ships woff2 for the web
// (web/static/fonts), a format gopdf cannot read.
//
// A later thermal-printer adapter would live in this same package, added
// purely alongside this file — the domain port is what makes that
// additive. Wiring PDFSheetRenderer into cmd/server and driving it from a
// batch-print operation is NSTR-50's job, not this ticket's.
package adapter
