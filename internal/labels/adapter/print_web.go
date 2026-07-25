package adapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestcore/render"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/labels/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
	storageapp "github.com/ericfisherdev/nestorage/internal/storage/app"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
	"github.com/ericfisherdev/nestorage/web/components"
)

// requestLayoutFunc wraps page content in the app shell for a specific
// request. A duplicate of storageadapter's own private type — not a shared
// import — for the identical reason resolveBaseURL is duplicated in
// labels_web.go's own doc: labels is a fresh bounded context, not an
// extension of storage. Injected by the composition root (cmd/server/shell.go).
type requestLayoutFunc func(r *http.Request, content templ.Component) templ.Component

// errBadTarget is answered as errBadRequest by handleTargetError when a
// /labels/print request names neither bin nor location, names both, or
// names one with a malformed id — distinguished from a visibility-driven
// 404 (resolveTarget's own doc) purely so the two failure modes map to
// their own correct HTTP statuses.
var errBadTarget = errors.New("labels: no single bin or location target given")

// printTarget is the resolved subject of a /labels/print request. Every
// print — whether reached from a bin's own detail page or a location's own
// index — ultimately renders through batchRenderer.RenderLocationBatch,
// which only ever takes a LocationID: NSTR-50 exposes no narrower
// "render just this one bin" operation, and NSTR-51's own plan is explicit
// that this ticket adds none ("this ticket's POST calls the same
// service"). A bin-scoped request therefore resolves to its own containing
// location for every subsequent step; what actually distinguishes the two
// entry points is cosmetic (HeaderTitle) and positional (InitialOffset
// preselects the tapped bin's own cell in the location's ordered bin list,
// rather than starting the sheet at cell zero) — the print screen's own
// LocationName/BinCount make the batch's real, location-wide scope visible
// either way, so a household member printing "just this one bin" still
// sees exactly what will land on the sheet.
type printTarget struct {
	// BinQuery/LocationQuery echo whichever of the two query parameters the
	// request actually named, so the print screen's own hidden inputs
	// (LabelSheetPreviewView.BinQuery/LocationQuery) carry the ORIGINAL
	// target forward through a preview refresh or a rejected submission,
	// re-resolving it identically next time rather than degrading a
	// bin-scoped request into a location-scoped one after one round trip.
	BinQuery      string
	LocationQuery string
	LocationID    storagedomain.LocationID
	LocationName  string
	// Bins is every bin viewer may see in LocationID, in
	// ListVisibleByLocation's own code order — the preview's own per-cell
	// occupancy source, and len(Bins) is the batch's real label count.
	Bins []storageapp.BinView
	// InitialOffset is 0 for a location target, and the tapped bin's own
	// index within Bins for a bin target — GET /labels/print's own starting
	// offset before the household member ever taps a different cell.
	InitialOffset int
	HeaderTitle   string
}

// resolveTarget resolves binQuery/locationQuery (GET /labels/print and GET
// /labels/print/preview's own query parameters, or POST /labels/print's
// identically-named hidden form fields) into a printTarget. Exactly one of
// binQuery/locationQuery must be non-empty — both set or both blank is
// errBadTarget. A bin or location viewer may not see answers wrapped
// storagedomain.ErrBinNotFound/ErrLocationNotFound, matching NSTR-50's own
// "unknown and invisible are indistinguishable" visibility contract (see
// storagedomain.BinRepository.FindVisibleByID's own doc) — handleTargetError
// maps both to a 404.
func (h *LabelsWebHandlers) resolveTarget(ctx context.Context, viewer identity.Principal, binQuery, locationQuery string) (printTarget, error) {
	switch {
	case binQuery != "" && locationQuery == "":
		return h.resolveBinTarget(ctx, viewer, binQuery)
	case locationQuery != "" && binQuery == "":
		return h.resolveLocationTarget(ctx, viewer, locationQuery)
	default:
		return printTarget{}, errBadTarget
	}
}

// resolveBinTarget resolves a bin-scoped target: the bin itself (for its
// own name and containing location id), then that location's own header
// and bin list — see printTarget's own doc for why the batch this ultimately
// feeds is location-wide, not filtered to the one bin.
func (h *LabelsWebHandlers) resolveBinTarget(ctx context.Context, viewer identity.Principal, binQuery string) (printTarget, error) {
	binID, err := storagedomain.ParseBinID(binQuery)
	if err != nil {
		return printTarget{}, errBadTarget
	}
	bin, err := h.bins.GetByID(ctx, viewer, binID)
	if err != nil {
		return printTarget{}, err
	}
	location, bins, err := h.locationAndBins(ctx, viewer, bin.Bin.LocationID)
	if err != nil {
		return printTarget{}, err
	}
	return printTarget{
		BinQuery: binQuery, LocationID: bin.Bin.LocationID, LocationName: location.Name,
		Bins: bins, InitialOffset: binOffset(bins, bin.Bin.ID),
		HeaderTitle: "Print label — " + bin.Bin.Name,
	}, nil
}

// resolveLocationTarget resolves a location-scoped target: the whole
// location's own header and bin list, offset always starting at the sheet's
// first cell.
func (h *LabelsWebHandlers) resolveLocationTarget(ctx context.Context, viewer identity.Principal, locationQuery string) (printTarget, error) {
	locationID, err := storagedomain.ParseLocationID(locationQuery)
	if err != nil {
		return printTarget{}, errBadTarget
	}
	location, bins, err := h.locationAndBins(ctx, viewer, locationID)
	if err != nil {
		return printTarget{}, err
	}
	return printTarget{
		LocationQuery: locationQuery, LocationID: locationID, LocationName: location.Name,
		Bins: bins, InitialOffset: 0,
		HeaderTitle: "Print labels — " + location.Name,
	}, nil
}

// locationAndBins loads locationID's own header data and its visible bins
// together — every resolveTarget path needs both, mirroring
// storageadapter.LocationsWebHandlers.renderDetail's identical shape.
func (h *LabelsWebHandlers) locationAndBins(ctx context.Context, viewer identity.Principal, locationID storagedomain.LocationID) (*storagedomain.Location, []storageapp.BinView, error) {
	location, err := h.locations.Get(ctx, viewer, locationID)
	if err != nil {
		return nil, nil, err
	}
	bins, err := h.locationBins.ListVisibleByLocation(ctx, viewer, locationID)
	if err != nil {
		return nil, nil, err
	}
	return location, bins, nil
}

// binOffset returns targetID's own index within bins (ListVisibleByLocation's
// own code order) — the cell a bin-scoped print screen preselects as its
// starting offset, so the very first sheet position shown is the requested
// bin's own label. Returns 0 (the sheet's first cell) if targetID is
// somehow absent from bins — defensive only; resolveBinTarget's own
// GetByID/ListVisibleByLocation calls share the identical visibility scope,
// so this should not happen in practice.
func binOffset(bins []storageapp.BinView, targetID storagedomain.BinID) int {
	for i, b := range bins {
		if b.Bin.ID == targetID {
			return i
		}
	}
	return 0
}

// preferredSizeID resolves viewer's remembered label size, falling back to
// the registry's own default (its first, stable List entry) when viewer has
// never printed (domain.ErrPreferenceNotFound), when the load itself fails
// (logged, not surfaced — a size preference is a convenience, not core to
// the page), or when the stored id no longer names a registered size (e.g.
// a removed builtin).
func (h *LabelsWebHandlers) preferredSizeID(ctx context.Context, viewer identity.Principal) domain.LabelSizeID {
	sizeID, err := h.preferences.Get(ctx, viewer.UserID)
	if err != nil {
		if !errors.Is(err, domain.ErrPreferenceNotFound) {
			h.logger.WarnContext(ctx, "labels: load size preference", "error", err)
		}
		return h.defaultSizeID()
	}
	if _, err := h.sizes.Get(sizeID); err != nil {
		return h.defaultSizeID()
	}
	return sizeID
}

// defaultSizeID returns the registry's own default: List's first entry, in
// its fixed declaration order (see domain.Registry.List's own doc for why
// that order is stable). "" for the pathological case of an empty registry,
// which NewRegistry's own fail-fast construction never actually produces in
// this codebase.
func (h *LabelsWebHandlers) defaultSizeID() domain.LabelSizeID {
	sizes := h.sizes.List()
	if len(sizes) == 0 {
		return ""
	}
	return sizes[0].ID
}

// PrintScreen handles GET /labels/print: the full print screen for a bin or
// location query parameter (resolveTarget's own doc). Loads viewer's
// remembered size, falling back to the registry default, with the offset
// preselected at the target's own InitialOffset.
func (h *LabelsWebHandlers) PrintScreen(w http.ResponseWriter, r *http.Request) {
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	target, err := h.resolveTarget(r.Context(), viewer, r.URL.Query().Get("bin"), r.URL.Query().Get("location"))
	if err != nil {
		h.handleTargetError(w, r, err)
		return
	}
	sizeID := h.preferredSizeID(r.Context(), viewer)
	h.renderPrintScreen(w, r, target, sizeID, target.InitialOffset, http.StatusOK, "")
}

// PrintPreview handles GET /labels/print/preview: the htmx fragment
// re-rendered on every size change and cell tap. Validates size against the
// registry and clamps offset to size's own per-sheet capacity — a tapped
// cell index, or an offset carried over from a size with a larger
// per-sheet capacity, can otherwise name a cell past the new size's own
// last valid one.
func (h *LabelsWebHandlers) PrintPreview(w http.ResponseWriter, r *http.Request) {
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	target, err := h.resolveTarget(r.Context(), viewer, r.URL.Query().Get("bin"), r.URL.Query().Get("location"))
	if err != nil {
		h.handleTargetError(w, r, err)
		return
	}
	size, err := h.sizes.Get(domain.LabelSizeID(r.URL.Query().Get("size")))
	if err != nil {
		http.Error(w, errBadRequest, http.StatusBadRequest)
		return
	}
	offset := clampOffset(parseOffsetOrZero(r.URL.Query().Get("offset")), size.CellsPerPage())

	view := buildPreviewView(target, size, offset)
	if err := render.Render(r.Context(), w, http.StatusOK, components.LabelSheetPreview(view)); err != nil {
		h.logger.ErrorContext(r.Context(), "labels: render preview", "error", err)
	}
}

// Print handles POST /labels/print: CSRF (this route's own mutation, unlike
// every other route in this package), resolve the target from the
// identically-named hidden form fields, persist viewer's size choice, then
// delegate to batchRenderer.RenderLocationBatch and stream its document
// through. A plain (non-htmx) form submission, never an htmx AJAX request —
// htmx's response cannot trigger a browser's native file-download behavior,
// so the PDF response with its Content-Disposition header must come from a
// native form post; htmx is used only for the live preview fragment
// (PrintPreview).
func (h *LabelsWebHandlers) Print(w http.ResponseWriter, r *http.Request) {
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	target, err := h.resolveTarget(r.Context(), viewer, r.FormValue("bin"), r.FormValue("location"))
	if err != nil {
		h.handleTargetError(w, r, err)
		return
	}
	sizeID := domain.LabelSizeID(r.FormValue("size_id"))
	size, err := h.sizes.Get(sizeID)
	if err != nil {
		h.renderPrintScreen(w, r, target, h.defaultSizeID(), target.InitialOffset, http.StatusUnprocessableEntity, "Please choose a valid label size.")
		return
	}
	offset := clampOffset(parseOffsetOrZero(r.FormValue("offset")), size.CellsPerPage())

	// Persisted before the render call, not after: NSTR-51's own plan calls
	// the size choice "proven real" the moment Print is submitted — a cap
	// or empty-batch error from the render below does not un-choose the
	// size the household member picked, so a resubmission (e.g. after
	// narrowing to a sub-location) still starts from it. Non-fatal to the
	// print itself: a failed write only costs the remembered-size
	// convenience next visit, not this print.
	if err := h.preferences.Upsert(r.Context(), viewer.UserID, sizeID); err != nil {
		h.logger.ErrorContext(r.Context(), "labels: persist size preference", "error", err)
	}

	batch, err := h.batches.RenderLocationBatch(r.Context(), viewer, target.LocationID, sizeID, offset, resolveBaseURL(r, h.publicBaseURL))
	if err != nil {
		status, msg, ok := mapPrintError(err)
		if !ok {
			h.logger.ErrorContext(r.Context(), "labels: print", "error", err)
			http.Error(w, errInternalServerError, http.StatusInternalServerError)
			return
		}
		h.renderPrintScreen(w, r, target, sizeID, offset, status, msg)
		return
	}
	writeDocumentResponse(w, r, h.logger, batch.Document, batch.Filename, "labels: write print response")
}

// renderPrintScreen builds and renders the full print screen — GET
// /labels/print, and a rejected POST /labels/print's own re-render — at
// status, carrying formError.
func (h *LabelsWebHandlers) renderPrintScreen(w http.ResponseWriter, r *http.Request, target printTarget, sizeID domain.LabelSizeID, offset int, status int, formError string) {
	size, err := h.sizes.Get(sizeID)
	if err != nil {
		// sizeID came from either defaultSizeID (which cannot fail against
		// the very registry it was read from) or a caller-supplied value
		// Print already validated before reaching here — unreachable in
		// practice, guarded so a future caller mistake fails safe rather
		// than panicking on a zero LabelSize.
		h.logger.ErrorContext(r.Context(), "labels: render print screen: unknown size", "error", err, "size_id", sizeID.String())
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	offset = clampOffset(offset, size.CellsPerPage())

	view := components.LabelPrintPageView{
		CSRFToken:    session.CSRFToken(r.Context(), h.sm),
		HeaderTitle:  target.HeaderTitle,
		LocationName: target.LocationName,
		BinCount:     len(target.Bins),
		Sizes:        buildSizeOptions(h.sizes.List(), size.ID),
		Preview:      buildPreviewView(target, size, offset),
		FormError:    formError,
	}
	content := components.LabelPrintPage(view)
	if !render.IsHTMX(r) {
		content = h.layout(r, content)
	}
	if err := render.Render(r.Context(), w, status, content); err != nil {
		h.logger.ErrorContext(r.Context(), "labels: render print screen", "error", err)
	}
}

// handleTargetError answers a failed resolveTarget call: 400 for
// errBadTarget (missing, duplicated, or malformed bin/location query
// parameters), 404 for a bin or location viewer may not see (matching
// NSTR-50's own "unknown and invisible are indistinguishable" visibility
// contract), logged 500 otherwise.
func (h *LabelsWebHandlers) handleTargetError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errBadTarget):
		http.Error(w, errBadRequest, http.StatusBadRequest)
	case errors.Is(err, storagedomain.ErrBinNotFound), errors.Is(err, storagedomain.ErrLocationNotFound):
		http.NotFound(w, r)
	default:
		h.logger.ErrorContext(r.Context(), "labels: resolve print target", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
	}
}

// mapPrintError maps a domain error from RenderLocationBatch to the status
// and inline message Print re-renders the print screen with — mirrors
// storageadapter's own mapBinError pattern (see its doc): ok reports
// whether err was recognized; an unrecognized error is the caller's cue to
// log it and answer a generic 500 instead.
func mapPrintError(err error) (status int, message string, ok bool) {
	var tooLarge *domain.BatchTooLargeError
	switch {
	case errors.Is(err, domain.ErrUnknownLabelSize):
		return http.StatusUnprocessableEntity, "Please choose a valid label size.", true
	case errors.Is(err, domain.ErrInvalidStartOffset):
		return http.StatusUnprocessableEntity, "That starting cell is no longer valid — pick a new one.", true
	case errors.Is(err, domain.ErrEmptyBatch):
		return http.StatusUnprocessableEntity, "There's nothing here to print — this location has no bins you can print.", true
	case errors.As(err, &tooLarge):
		return http.StatusUnprocessableEntity, fmt.Sprintf("This location has %d bins; batches are limited to %d. Print a sub-location instead.", tooLarge.Count, tooLarge.Max), true
	case errors.Is(err, storagedomain.ErrLocationNotFound):
		return http.StatusNotFound, "That location no longer exists.", true
	default:
		return 0, "", false
	}
}

// buildPreviewView projects target/size/offset into the preview fragment's
// view model — the single geometry source (CellOrigin, this package's own
// NSTR-49 PDF-renderer function, pdf_sheet.go) both the on-screen preview
// and the printed PDF read from, so the "preview matches the produced PDF"
// criterion holds by construction. Pure: no I/O, so it is unit-tested
// directly, including the offset-skips-cells case.
func buildPreviewView(target printTarget, size domain.LabelSize, offset int) components.LabelSheetPreviewView {
	cellsPerPage := size.CellsPerPage()
	cells := make([]components.LabelPreviewCellView, cellsPerPage)
	for i := range cells {
		cells[i] = previewCell(size, target.LocationName, target.Bins, offset, i)
	}
	return components.LabelSheetPreviewView{
		BinQuery: target.BinQuery, LocationQuery: target.LocationQuery,
		SizeID: size.ID.String(), Offset: offset,
		PageWidthMM: size.PageWidthMM, PageHeightMM: size.PageHeightMM,
		Cells:   cells,
		Summary: summaryLine(len(target.Bins), sheetsNeeded(offset, len(target.Bins), cellsPerPage)),
	}
}

// previewCell builds one sheet cell's own view: its position (CellOrigin's
// millimetre origin, converted to page-relative percentages) and its
// occupancy — dimmed before offset, filled with the bin that would print
// there, or (past the batch's own last label, on a sheet with spare
// capacity) plain and empty.
func previewCell(size domain.LabelSize, locationName string, bins []storageapp.BinView, offset, index int) components.LabelPreviewCellView {
	x, y := CellOrigin(size, index)
	cell := components.LabelPreviewCellView{
		Index:         index,
		LeftPercent:   x / size.PageWidthMM * 100,
		TopPercent:    y / size.PageHeightMM * 100,
		WidthPercent:  size.CellWidthMM / size.PageWidthMM * 100,
		HeightPercent: size.CellHeightMM / size.PageHeightMM * 100,
	}
	switch {
	case index < offset:
		cell.Dimmed = true
	case index-offset < len(bins):
		b := bins[index-offset]
		cell.Filled = true
		cell.BinCode = b.Bin.Code
		cell.BinName = b.Bin.Name
		cell.LocationName = locationName
	}
	return cell
}

// sheetsNeeded returns how many sheets binCount labels need, starting at
// offset's own first-page cell — the print screen's "N labels across M
// sheets" summary. 0 when binCount is 0 (nothing to print).
func sheetsNeeded(offset, binCount, cellsPerPage int) int {
	if binCount == 0 {
		return 0
	}
	total := offset + binCount
	return (total + cellsPerPage - 1) / cellsPerPage
}

// summaryLine renders the preview's own "N labels across M sheets" line.
func summaryLine(labelCount, sheets int) string {
	switch labelCount {
	case 0:
		return "No labels to print yet."
	case 1:
		return fmt.Sprintf("1 label across %d sheet%s", sheets, plural(sheets))
	default:
		return fmt.Sprintf("%d labels across %d sheet%s", labelCount, sheets, plural(sheets))
	}
}

// plural returns "s" unless n is exactly 1.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// clampOffset bounds offset to 0..cellsPerPage-1 — the valid range
// domain.LabelRenderer.Render itself enforces (ErrInvalidStartOffset), so a
// tapped cell index or an offset carried over from a different size never
// reaches the renderer out of range.
func clampOffset(offset, cellsPerPage int) int {
	if offset < 0 {
		return 0
	}
	if cellsPerPage > 0 && offset >= cellsPerPage {
		return cellsPerPage - 1
	}
	return offset
}

// parseOffsetOrZero parses raw as an offset, defaulting to 0 for an empty or
// malformed value. Unlike Download's own parseStartOffset, a malformed
// preview/print offset degrades to the first cell rather than failing the
// request outright: PrintPreview's fragment and Print's own resubmission
// are both driven by this package's own generated hidden inputs, never a
// hand-typed URL, so a parse failure here is defensive rather than expected.
func parseOffsetOrZero(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return n
}

// buildSizeOptions projects the registry's own List into the size picker's
// view model, marking selectedID's own entry Selected.
func buildSizeOptions(sizes []domain.LabelSize, selectedID domain.LabelSizeID) []components.LabelSizeOptionView {
	opts := make([]components.LabelSizeOptionView, 0, len(sizes))
	for _, s := range sizes {
		opts = append(opts, components.LabelSizeOptionView{
			ID: s.ID.String(), DisplayName: s.DisplayName,
			CellWidthMM: s.CellWidthMM, CellHeightMM: s.CellHeightMM,
			PerSheetCount: s.CellsPerPage(), Selected: s.ID == selectedID,
		})
	}
	return opts
}

// verifyRequest parses the form and verifies the CSRF token — Print's own
// pre-check before doing anything else. A duplicate of
// storageadapter.verifyRequest — not a shared import — for the identical
// reason resolveBaseURL is duplicated in labels_web.go's own doc: that
// function is unexported to storage/adapter's own package.
func verifyRequest(w http.ResponseWriter, r *http.Request, sm *scs.SessionManager) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, errBadRequest, http.StatusBadRequest)
		return false
	}
	if !session.VerifyCSRF(r, sm) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return false
	}
	return true
}
