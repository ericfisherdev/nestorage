package adapter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestcore/httpserver/middleware"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	labelsapp "github.com/ericfisherdev/nestorage/internal/labels/app"
	"github.com/ericfisherdev/nestorage/internal/labels/domain"
	storageapp "github.com/ericfisherdev/nestorage/internal/storage/app"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// errInternalServerError is the generic message every unexpected failure in
// this package's web handlers answers with. Mirrors storage/adapter's own
// constant of the same name — that one is unexported to its package, so it
// cannot be reused directly here (see that constant's own doc for the
// identical rationale).
const errInternalServerError = "internal server error"

// errBadRequest is the message every route in this package answers with for
// a malformed input rejected before ever calling a service — a malformed
// path id, a missing size, an unparsable offset, or an unrecognized print
// target. Named once, not repeated per call site (SonarCloud flagged the
// four-way duplication, go:S1192).
const errBadRequest = "bad request"

// batchRenderer is the narrow port (ISP) LabelsWebHandlers depends on,
// satisfied by *labelsapp.BatchService. Both Download (NSTR-50) and Print
// (NSTR-51) render through this same service — see printTarget's own doc
// (print_web.go) for why NSTR-51's screen, even when printing a single bin,
// resolves to a LocationID before calling it.
type batchRenderer interface {
	RenderLocationBatch(ctx context.Context, viewer identity.Principal, locationID storagedomain.LocationID, sizeID domain.LabelSizeID, startOffset int, baseURL string) (*labelsapp.BatchDocument, error)
}

// binGetter is the narrow port (ISP) LabelsWebHandlers depends on to
// resolve a bin-scoped print target, satisfied by *storageapp.BinService (a
// superset, via GetByID). A bin viewer may not see 404s here exactly as
// GetByID's own visibility contract promises, matching NSTR-50's own
// filtering.
type binGetter interface {
	GetByID(ctx context.Context, viewer identity.Principal, id storagedomain.BinID) (*storageapp.BinView, error)
}

// locationGetter is the narrow port (ISP) LabelsWebHandlers depends on to
// resolve a location-scoped print target (and a bin-scoped one's own
// containing location), satisfied by *storageapp.LocationService (a
// superset, via Get).
type locationGetter interface {
	Get(ctx context.Context, viewer identity.Principal, id storagedomain.LocationID) (*storagedomain.Location, error)
}

// locationBinLister is the narrow port (ISP) LabelsWebHandlers depends on to
// build the print screen's own preview (which bin occupies which cell, and
// how many sheets the batch needs), satisfied by *storageapp.BinService (a
// superset, via ListVisibleByLocation) — a second, independent read from the
// same visibility-scoped query batchRenderer's own RenderLocationBatch
// performs internally, since the preview must answer without ever spending
// PDF-render time and must stay correct for a size/offset combination the
// user has not yet submitted.
type locationBinLister interface {
	ListVisibleByLocation(ctx context.Context, viewer identity.Principal, locationID storagedomain.LocationID) ([]storageapp.BinView, error)
}

// LabelsWebHandlers serves NSTR-50's batch label download (GET
// /locations/{id}/labels.pdf) and NSTR-51's print screen (GET /labels/print,
// GET /labels/print/preview, POST /labels/print). Mirrors
// storageadapter.BinsWebHandlers' own shape: narrow service interfaces
// (ISP), an injected requestLayoutFunc, the session manager for CSRF on the
// one mutating route, a logger; the constructor panics on any nil
// dependency (DIP + fail-fast). Every route is registered on its own mux,
// mounted behind RequireAuthenticated by the composition root
// (cmd/server/shell.go).
type LabelsWebHandlers struct {
	bins         binGetter
	locations    locationGetter
	locationBins locationBinLister
	sizes        *domain.Registry
	preferences  domain.SizePreferenceRepository
	batches      batchRenderer
	sm           *scs.SessionManager
	layout       requestLayoutFunc
	logger       *slog.Logger
	// publicBaseURL is PUBLIC_BASE_URL (corecfg.ServerConfig.PublicBaseURL),
	// resolved per request against resolveBaseURL for each label's QR deep
	// link (NSTR-48). Empty is a legitimate, common value — it means
	// "derive the origin from each request instead" — so, unlike every
	// other field above, NewLabelsWebHandlers does not panic on it being
	// unset; mirrors storageadapter.BinsWebHandlers.publicBaseURL's
	// identical rationale.
	publicBaseURL string
}

// LabelsWebHandlersDeps groups NewLabelsWebHandlers' dependencies into one
// value instead of a growing parameter list — mirrors
// storageadapter.BinsWebHandlersDeps' own rationale (see its doc). Every
// field is still injected explicitly by the composition root
// (cmd/server/main.go); this is a grouping of constructor arguments, not a
// service locator.
type LabelsWebHandlersDeps struct {
	Bins         binGetter
	Locations    locationGetter
	LocationBins locationBinLister
	Sizes        *domain.Registry
	Preferences  domain.SizePreferenceRepository
	Batches      batchRenderer
	SM           *scs.SessionManager
	Layout       requestLayoutFunc
	Logger       *slog.Logger
	// PublicBaseURL is PUBLIC_BASE_URL — see LabelsWebHandlers.publicBaseURL's
	// own doc for why this is the one field NewLabelsWebHandlers allows empty.
	PublicBaseURL string
}

// NewLabelsWebHandlers constructs LabelsWebHandlers. Every dependency but
// PublicBaseURL is required; a missing one panics at construction time,
// matching every other WebHandlers constructor in this codebase.
func NewLabelsWebHandlers(deps LabelsWebHandlersDeps) *LabelsWebHandlers {
	if deps.Bins == nil {
		panic("labels/adapter: NewLabelsWebHandlers requires a non-nil binGetter")
	}
	if deps.Locations == nil {
		panic("labels/adapter: NewLabelsWebHandlers requires a non-nil locationGetter")
	}
	if deps.LocationBins == nil {
		panic("labels/adapter: NewLabelsWebHandlers requires a non-nil locationBinLister")
	}
	if deps.Sizes == nil {
		panic("labels/adapter: NewLabelsWebHandlers requires a non-nil *domain.Registry")
	}
	if deps.Preferences == nil {
		panic("labels/adapter: NewLabelsWebHandlers requires a non-nil domain.SizePreferenceRepository")
	}
	if deps.Batches == nil {
		panic("labels/adapter: NewLabelsWebHandlers requires a non-nil batchRenderer")
	}
	if deps.SM == nil {
		panic("labels/adapter: NewLabelsWebHandlers requires a non-nil session manager")
	}
	if deps.Layout == nil {
		panic("labels/adapter: NewLabelsWebHandlers requires a non-nil layout func")
	}
	if deps.Logger == nil {
		panic("labels/adapter: NewLabelsWebHandlers requires a non-nil logger")
	}
	return &LabelsWebHandlers{
		bins: deps.Bins, locations: deps.Locations, locationBins: deps.LocationBins,
		sizes: deps.Sizes, preferences: deps.Preferences, batches: deps.Batches,
		sm: deps.SM, layout: deps.Layout, logger: deps.Logger, publicBaseURL: deps.PublicBaseURL,
	}
}

// Routes registers every route this handler group serves on mux: NSTR-50's
// batch download (a GET — rendering a batch persists nothing, so it carries
// none of a mutation's CSRF requirement) and NSTR-51's print screen (whose
// one mutating route, POST /labels/print, does carry that requirement — see
// Print's own doc, print_web.go).
func (h *LabelsWebHandlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /locations/{id}/labels.pdf", h.Download)
	mux.HandleFunc("GET /labels/print", h.PrintScreen)
	mux.HandleFunc("GET /labels/print/preview", h.PrintPreview)
	mux.HandleFunc("POST /labels/print", h.Print)
}

// Download handles GET /locations/{id}/labels.pdf: parses the path id and
// the size/offset query parameters, renders the batch, and streams it back
// as an attachment. Query params: size (a registry LabelSizeID, required)
// and offset (the first sheet's start cell, default 0 — validated against
// size's own per-sheet capacity by the renderer this delegates to; see
// labelsapp.BatchService.RenderLocationBatch's own doc).
func (h *LabelsWebHandlers) Download(w http.ResponseWriter, r *http.Request) {
	locationID, err := storagedomain.ParseLocationID(r.PathValue("id"))
	if err != nil {
		http.Error(w, errBadRequest, http.StatusBadRequest)
		return
	}
	sizeID := domain.LabelSizeID(r.URL.Query().Get("size"))
	if sizeID == "" {
		http.Error(w, errBadRequest, http.StatusBadRequest)
		return
	}
	startOffset, ok := parseStartOffset(r.URL.Query().Get("offset"))
	if !ok {
		http.Error(w, errBadRequest, http.StatusBadRequest)
		return
	}

	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	baseURL := resolveBaseURL(r, h.publicBaseURL)

	batch, err := h.batches.RenderLocationBatch(r.Context(), viewer, locationID, sizeID, startOffset, baseURL)
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	writeDocumentResponse(w, r, h.logger, batch.Document, batch.Filename, "labels: write batch response")
}

// parseStartOffset parses the offset query parameter: "" defaults to 0 (the
// first cell), otherwise it must parse as an integer — a negative or
// out-of-range value is still let through here and caught later by the
// renderer's own domain.ErrInvalidStartOffset check (see
// labelsapp.BatchService.RenderLocationBatch's doc), since only the
// resolved LabelSize's per-sheet capacity (not this handler) knows the
// valid upper bound. ok is false only for a value that fails to parse as an
// integer at all.
func parseStartOffset(raw string) (offset int, ok bool) {
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return n, true
}

// handleRenderError maps a failed RenderLocationBatch call to the plain-text
// response NSTR-50's plan specifies — there is no page to re-render a form
// error into, unlike Print's own error mapping (mapPrintError, print_web.go).
func (h *LabelsWebHandlers) handleRenderError(w http.ResponseWriter, r *http.Request, err error) {
	var tooLarge *domain.BatchTooLargeError
	switch {
	case errors.Is(err, storagedomain.ErrLocationNotFound):
		http.NotFound(w, r)
	case errors.Is(err, domain.ErrUnknownLabelSize), errors.Is(err, domain.ErrInvalidStartOffset):
		http.Error(w, errBadRequest, http.StatusBadRequest)
	case errors.Is(err, domain.ErrEmptyBatch):
		http.Error(w, "This location has no bins you can print.", http.StatusUnprocessableEntity)
	case errors.As(err, &tooLarge):
		http.Error(w, fmt.Sprintf("This location has %d bins; batches are limited to %d. Print a sub-location instead.", tooLarge.Count, tooLarge.Max), http.StatusUnprocessableEntity)
	default:
		h.logger.ErrorContext(r.Context(), "labels: render batch", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
	}
}

// writeDocumentResponse streams doc as an attachment download named
// filename — the response-writing tail Download and Print (POST
// /labels/print) share, since both ultimately answer with the exact same
// shape of response: a fully materialized document plus its suggested
// filename.
func writeDocumentResponse(w http.ResponseWriter, r *http.Request, logger *slog.Logger, doc domain.Document, filename string, op string) {
	w.Header().Set("Content-Type", doc.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	// Content-Length is set explicitly: doc.Data is a fully materialized
	// []byte at this point (the renderer already built the whole document
	// in memory), so its size is known up front — without this header,
	// net/http falls back to chunked transfer encoding and the browser
	// loses determinate download progress for no reason.
	w.Header().Set("Content-Length", strconv.Itoa(len(doc.Data)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(doc.Data); err != nil {
		logger.ErrorContext(r.Context(), op, "error", err)
	}
}

// resolveBaseURL resolves the origin app.BinDeepLinkURL builds each label's
// QR deep link against. A duplicate of storageadapter.resolveBaseURL — not
// a shared import of it — deliberately: that function is unexported to
// storage/adapter's own package, and this bounded context does not reach
// into another one's adapter internals to save a dozen lines (labels is "a
// fresh bounded context, not an extension of storage", per this package's
// own doc). This is the same duplication-over-coupling call already made
// between Nestova's and Nestorage's own kiosk/bin QR handlers — see
// storage/adapter.resolveBaseURL's own doc for the identical rationale
// mirrored here.
func resolveBaseURL(r *http.Request, publicBaseURL string) string {
	if publicBaseURL != "" {
		return publicBaseURL
	}
	scheme := middleware.RequestScheme(r.Context())
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host
}
