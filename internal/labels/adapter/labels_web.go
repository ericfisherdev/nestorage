package adapter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/ericfisherdev/nestcore/httpserver/middleware"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	labelsapp "github.com/ericfisherdev/nestorage/internal/labels/app"
	"github.com/ericfisherdev/nestorage/internal/labels/domain"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// errInternalServerError is the generic message every unexpected failure in
// this package's web handlers answers with. Mirrors storage/adapter's own
// constant of the same name — that one is unexported to its package, so it
// cannot be reused directly here (see that constant's own doc for the
// identical rationale).
const errInternalServerError = "internal server error"

// errBadRequest is the message Download answers with for every malformed
// input it rejects before ever calling batches — a malformed path id, a
// missing size, an unparsable offset, or (via handleRenderError) a size/
// offset the batch renderer itself rejected. Named once, not repeated per
// call site (SonarCloud flagged the four-way duplication, go:S1192).
const errBadRequest = "bad request"

// batchRenderer is the narrow port (ISP) LabelsWebHandlers depends on,
// satisfied by *labelsapp.BatchService.
type batchRenderer interface {
	RenderLocationBatch(ctx context.Context, viewer identity.Principal, locationID storagedomain.LocationID, sizeID domain.LabelSizeID, startOffset int, baseURL string) (*labelsapp.BatchDocument, error)
}

// LabelsWebHandlers serves NSTR-50's batch label download: GET
// /locations/{id}/labels.pdf. Mirrors storageadapter.LocationsWebHandlers'
// shape (constructor panics on nil deps, unexported narrow port over
// *labelsapp.BatchService) but carries no session manager or layout: every
// route here answers a plain file download, never an HTML page, so there is
// nothing to render into and no form to CSRF-check (see Routes' own doc).
type LabelsWebHandlers struct {
	batches batchRenderer
	logger  *slog.Logger
	// publicBaseURL is PUBLIC_BASE_URL (corecfg.ServerConfig.PublicBaseURL),
	// resolved per request against resolveBaseURL for each label's QR deep
	// link (NSTR-48). Empty is a legitimate, common value — it means
	// "derive the origin from each request instead" — so, unlike batches
	// and logger, NewLabelsWebHandlers does not panic on it being unset;
	// mirrors storageadapter.BinsWebHandlers.publicBaseURL's identical
	// rationale.
	publicBaseURL string
}

// NewLabelsWebHandlers constructs LabelsWebHandlers. batches and logger are
// required; a missing one panics at construction time, matching every
// other WebHandlers constructor in this codebase.
func NewLabelsWebHandlers(batches batchRenderer, logger *slog.Logger, publicBaseURL string) *LabelsWebHandlers {
	if batches == nil {
		panic("labels/adapter: NewLabelsWebHandlers requires a non-nil batchRenderer")
	}
	if logger == nil {
		panic("labels/adapter: NewLabelsWebHandlers requires a non-nil logger")
	}
	return &LabelsWebHandlers{batches: batches, logger: logger, publicBaseURL: publicBaseURL}
}

// Routes registers the batch label download route on mux. GET, not POST:
// rendering a batch is a read (it persists nothing), so it carries none of
// a mutation's CSRF requirement, and a GET is what lets the browser treat
// the response as a plain download and NSTR-51's print action link straight
// to it.
func (h *LabelsWebHandlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /locations/{id}/labels.pdf", h.Download)
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

	w.Header().Set("Content-Type", batch.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", batch.Filename))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(batch.Data); err != nil {
		h.logger.ErrorContext(r.Context(), "labels: write batch response", "error", err)
	}
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
// error into, unlike storageadapter.LocationsWebHandlers' own error
// mapping.
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

// resolveBaseURL resolves the origin BinDeepLinkURL builds each label's QR
// deep link against. A duplicate of storageadapter.resolveBaseURL — not a
// shared import of it — deliberately: that function is unexported to
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
