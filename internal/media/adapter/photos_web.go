package adapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestcore/render"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	mediaapp "github.com/ericfisherdev/nestorage/internal/media/app"
	"github.com/ericfisherdev/nestorage/internal/media/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
	"github.com/ericfisherdev/nestorage/web/components"
)

// errInternalServerError is the generic message every unexpected failure in
// this package's web handlers answers with. Mirrors storage/adapter's own
// constant of the same name — that one is unexported to its package, so it
// cannot be reused directly here.
const errInternalServerError = "internal server error"

// multipartMaxMemory bounds ParseMultipartForm's in-memory buffer, matching
// net/http's own ParseMultipartForm default (32 MiB) — an upload larger
// than this spills to a temp file that net/http itself manages, so this is
// unrelated to (and always at least as generous as) MEDIA_MAX_UPLOAD_BYTES,
// which mediaapp.PhotoService.Upload enforces per file, inside its own
// ValidateAndStage call, regardless of how ParseMultipartForm staged the
// bytes.
const multipartMaxMemory = 32 << 20

// photoFileField is the multipart field name both the picker input
// (type=file multiple) and the camera-capture input (capture=environment)
// in web/components/photo_gallery.templ's upload form share — the ticket's
// own reconciliation calls for two separate <input> elements, but one
// shared field name so Upload reads every selected file from a single
// r.MultipartForm.File lookup.
const photoFileField = "photos"

// photoOperator is the narrow port (ISP) PhotosWebHandlers depends on for
// every photo read/mutation, satisfied by *mediaapp.PhotoService — NSTR-34's
// own complete media application service (Sprint 5 reconciliation R1: this
// ticket writes no application service of its own, only this web layer on
// top of it).
type photoOperator interface {
	Upload(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, r io.Reader) (*domain.Photo, error)
	ListForItem(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID) ([]domain.ItemPhoto, error)
	Open(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, photoID domain.PhotoID, variant domain.PhotoVariant) (io.ReadCloser, string, error)
	DirectURL(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, photoID domain.PhotoID, variant domain.PhotoVariant) (string, error)
	SupportsDirectURL() bool
	Delete(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, photoID domain.PhotoID) error
	SetPrimary(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, photoID domain.PhotoID) error
	Reorder(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, order []domain.PhotoID) error
}

// Compile-time assurance *mediaapp.PhotoService satisfies photoOperator.
var _ photoOperator = (*mediaapp.PhotoService)(nil)

// PhotosWebHandlers serves the item photo upload/gallery/serve routes
// (NSTR-37). Unlike every other WebHandlers type in this codebase, it
// carries no requestLayoutFunc: every response this type renders is either
// binary image bytes (Serve) or an HTML fragment (List/Upload/SetPrimary/
// Move/Delete) — the gallery is embedded in item_detail.templ as a
// self-contained stub that HTMX-loads its own content (Sprint 5
// reconciliation R4), so no route here is ever a full-page navigation that
// would need the app shell wrapped around it. The constructor panics on any
// nil dependency, matching every other WebHandlers constructor in this
// codebase.
type PhotosWebHandlers struct {
	photos photoOperator
	sm     *scs.SessionManager
	logger *slog.Logger
}

// PhotosWebHandlersDeps groups NewPhotosWebHandlers' dependencies into one
// value, mirroring storage/adapter's own *WebHandlersDeps grouping
// rationale. Every field is still injected explicitly by the composition
// root (cmd/server/main.go).
type PhotosWebHandlersDeps struct {
	Photos photoOperator
	SM     *scs.SessionManager
	Logger *slog.Logger
}

// NewPhotosWebHandlers constructs PhotosWebHandlers. All dependencies are
// required; a missing one panics at construction time, matching every other
// WebHandlers constructor in this codebase.
func NewPhotosWebHandlers(deps PhotosWebHandlersDeps) *PhotosWebHandlers {
	if deps.Photos == nil {
		panic("media/adapter: NewPhotosWebHandlers requires a non-nil photoOperator")
	}
	if deps.SM == nil {
		panic("media/adapter: NewPhotosWebHandlers requires a non-nil session manager")
	}
	if deps.Logger == nil {
		panic("media/adapter: NewPhotosWebHandlers requires a non-nil logger")
	}
	return &PhotosWebHandlers{photos: deps.Photos, sm: deps.SM, logger: deps.Logger}
}

// Routes registers the item photo routes on mux. GET /items/{id}/photos and
// POST /items/{id}/photos share one path but distinct methods (Sprint 5
// reconciliation R4): the GET serves both the gallery stub's initial
// hx-trigger="load" and every mutation's own outerHTML re-render, the POST
// is the multipart upload. None of these collide with
// storage/adapter.ItemsWebHandlers' own /items/{id} or NSTR-38's
// /items/{id}/links... patterns.
func (h *PhotosWebHandlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /items/{id}/photos", h.List)
	mux.HandleFunc("POST /items/{id}/photos", h.Upload)
	mux.HandleFunc("GET /items/{id}/photos/{photoID}", h.Serve)
	mux.HandleFunc("POST /items/{id}/photos/{photoID}/primary", h.SetPrimary)
	mux.HandleFunc("POST /items/{id}/photos/{photoID}/move", h.Move)
	mux.HandleFunc("POST /items/{id}/photos/{photoID}/delete", h.Delete)
}

// List handles GET /items/{id}/photos: the gallery stub's initial
// hx-trigger="load" fetch, rendered with no pending errors.
func (h *PhotosWebHandlers) List(w http.ResponseWriter, r *http.Request) {
	itemID, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	h.renderGallery(w, r, viewer, itemID, http.StatusOK, nil)
}

// Upload handles POST /items/{id}/photos: a multipart upload of one or more
// files under photoFileField, each streamed individually into
// PhotoService.Upload. Per Sprint 5 reconciliation R6, the response is
// always one gallery fragment: 200 when at least one file succeeded (with
// any failures listed), 422 when every file failed (including "no files
// selected").
func (h *PhotosWebHandlers) Upload(w http.ResponseWriter, r *http.Request) {
	itemID, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	if !verifyUploadRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	var files []*multipart.FileHeader
	if r.MultipartForm != nil {
		files = r.MultipartForm.File[photoFileField]
	}

	var uploadErrors []string
	succeeded := 0
	for _, fh := range files {
		if err := h.uploadOne(r.Context(), viewer, itemID, fh); err != nil {
			_, msg, mapped := mapPhotoError(err)
			if !mapped {
				h.logger.ErrorContext(r.Context(), "media: photos: upload", "error", err)
				msg = errInternalServerError
			}
			uploadErrors = append(uploadErrors, fh.Filename+": "+msg)
			continue
		}
		succeeded++
	}

	status := http.StatusOK
	switch {
	case len(files) == 0:
		uploadErrors = append(uploadErrors, "Please choose at least one photo.")
		status = http.StatusUnprocessableEntity
	case succeeded == 0:
		status = http.StatusUnprocessableEntity
	}
	h.renderGallery(w, r, viewer, itemID, status, uploadErrors)
}

// uploadOne opens fh and streams it into PhotoService.Upload, wrapping an
// unreadable multipart part as domain.ErrInvalidPhoto so it still maps to a
// per-file 400 rather than falling through to Upload's caller-side "log and
// 500" branch.
func (h *PhotosWebHandlers) uploadOne(ctx context.Context, viewer identity.Principal, itemID storagedomain.ItemID, fh *multipart.FileHeader) error {
	f, err := fh.Open()
	if err != nil {
		return fmt.Errorf("%w: could not read the uploaded file", domain.ErrInvalidPhoto)
	}
	defer func() { _ = f.Close() }()
	_, err = h.photos.Upload(ctx, viewer, itemID, f)
	return err
}

// Serve handles GET /items/{id}/photos/{photoID}: the size query parameter
// (thumb|full, default full) selects the variant, passed straight through
// to PhotoService.Open/DirectURL — a photo whose thumbnail does not exist
// resolves to the full image INSIDE the service, so this handler never
// branches on that itself (Sprint 5 reconciliation R8). R7 is the load-
// bearing branch: SupportsDirectURL true (S3) means a 302 redirect to
// DirectURL's presigned locator; false (local) means streaming Open's bytes
// through this process. LocalPhotoStore's own URL method returns a
// deliberately non-navigable locator and must never reach a browser, so
// DirectURL is only ever called in the true branch.
func (h *PhotosWebHandlers) Serve(w http.ResponseWriter, r *http.Request) {
	itemID, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	photoID, ok := h.pathPhotoID(w, r)
	if !ok {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	variant := domain.PhotoVariantFull
	if sizeParam := r.URL.Query().Get("size"); sizeParam != "" {
		v, err := domain.ParsePhotoVariant(sizeParam)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		variant = v
	}

	if h.photos.SupportsDirectURL() {
		url, err := h.photos.DirectURL(r.Context(), viewer, itemID, photoID, variant)
		if err != nil {
			h.handlePhotoGetError(w, r, err, "media: photos: direct url")
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
		return
	}

	rc, contentType, err := h.photos.Open(r.Context(), viewer, itemID, photoID, variant)
	if err != nil {
		h.handlePhotoGetError(w, r, err, "media: photos: open")
		return
	}
	defer func() { _ = rc.Close() }()
	w.Header().Set("Content-Type", contentType)
	if _, err := io.Copy(w, rc); err != nil {
		h.logger.ErrorContext(r.Context(), "media: photos: stream", "error", err)
	}
}

// SetPrimary handles POST /items/{id}/photos/{photoID}/primary:
// CSRF-verified, PhotoService.SetPrimary, then re-render the gallery
// fragment either way — mirrors storage/adapter.ItemsWebHandlers.CheckOut's
// own shape (Sprint 5 reconciliation R1/R6).
func (h *PhotosWebHandlers) SetPrimary(w http.ResponseWriter, r *http.Request) {
	itemID, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	photoID, ok := h.pathPhotoID(w, r)
	if !ok {
		return
	}
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	if err := h.photos.SetPrimary(r.Context(), viewer, itemID, photoID); err != nil {
		h.renderMutationError(w, r, viewer, itemID, err, "media: photos: set primary")
		return
	}
	h.renderGallery(w, r, viewer, itemID, http.StatusOK, nil)
}

// Move handles POST /items/{id}/photos/{photoID}/move: CSRF-verified, swaps
// photoID with its immediate neighbor in the direction the dir form value
// names (up|down), then calls PhotoService.Reorder with the resulting full
// order — PhotoService exposes only whole-order Reorder, not a single-step
// primitive, so this handler is what turns one button click into that full
// order (see moveOrder's own doc). Re-renders the gallery fragment either
// way, mirroring SetPrimary's own shape.
func (h *PhotosWebHandlers) Move(w http.ResponseWriter, r *http.Request) {
	itemID, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	photoID, ok := h.pathPhotoID(w, r)
	if !ok {
		return
	}
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	current, err := h.photos.ListForItem(r.Context(), viewer, itemID)
	if err != nil {
		h.renderMutationError(w, r, viewer, itemID, err, "media: photos: move: list")
		return
	}
	order, err := moveOrder(current, photoID, r.FormValue("dir"))
	if err != nil {
		h.renderMutationError(w, r, viewer, itemID, err, "media: photos: move: compute order")
		return
	}
	if err := h.photos.Reorder(r.Context(), viewer, itemID, order); err != nil {
		h.renderMutationError(w, r, viewer, itemID, err, "media: photos: move: reorder")
		return
	}
	h.renderGallery(w, r, viewer, itemID, http.StatusOK, nil)
}

// Delete handles POST /items/{id}/photos/{photoID}/delete: CSRF-verified,
// PhotoService.Delete, then re-render the gallery fragment either way,
// mirroring SetPrimary's own shape.
func (h *PhotosWebHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	itemID, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	photoID, ok := h.pathPhotoID(w, r)
	if !ok {
		return
	}
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	if err := h.photos.Delete(r.Context(), viewer, itemID, photoID); err != nil {
		h.renderMutationError(w, r, viewer, itemID, err, "media: photos: delete")
		return
	}
	h.renderGallery(w, r, viewer, itemID, http.StatusOK, nil)
}

// pathItemID parses the {id} path value, answering 400 and reporting
// ok=false on a malformed one — mirrors storage/adapter's own pathItemID
// (a different package, so it cannot be reused directly here).
func (h *PhotosWebHandlers) pathItemID(w http.ResponseWriter, r *http.Request) (storagedomain.ItemID, bool) {
	id, err := storagedomain.ParseItemID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return storagedomain.ItemID{}, false
	}
	return id, true
}

// pathPhotoID parses the {photoID} path value, answering 400 and reporting
// ok=false on a malformed one.
func (h *PhotosWebHandlers) pathPhotoID(w http.ResponseWriter, r *http.Request) (domain.PhotoID, bool) {
	id, err := domain.ParsePhotoID(r.PathValue("photoID"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return domain.PhotoID{}, false
	}
	return id, true
}

// renderMutationError maps err (a SetPrimary/Move/Delete failure) to a
// status and message and re-renders the gallery fragment carrying it, or
// logs and answers a generic 500 for an unrecognized error — the shared
// tail SetPrimary/Move/Delete all need, mirroring
// storage/adapter.ItemsWebHandlers.CheckOut's identical map-then-rerender
// shape.
func (h *PhotosWebHandlers) renderMutationError(w http.ResponseWriter, r *http.Request, viewer identity.Principal, itemID storagedomain.ItemID, err error, op string) {
	status, msg, mapped := mapPhotoError(err)
	if !mapped {
		h.logger.ErrorContext(r.Context(), op, "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	h.renderGallery(w, r, viewer, itemID, status, []string{msg})
}

// renderGallery loads itemID's current photos and renders the gallery
// fragment at status, carrying errs (nil for none) — List's own tail, and
// every mutation's shared re-render after a successful or rejected
// operation. Always a bare fragment, never wrapped in the app shell (see
// PhotosWebHandlers' own doc for why this type carries no
// requestLayoutFunc).
func (h *PhotosWebHandlers) renderGallery(w http.ResponseWriter, r *http.Request, viewer identity.Principal, itemID storagedomain.ItemID, status int, errs []string) {
	photos, err := h.photos.ListForItem(r.Context(), viewer, itemID)
	if err != nil {
		h.handlePhotoGetError(w, r, err, "media: photos: list")
		return
	}
	view := buildPhotoGalleryView(itemID, photos, session.CSRFToken(r.Context(), h.sm), errs)
	if err := render.Render(r.Context(), w, status, components.PhotoGallerySection(view)); err != nil {
		h.logger.ErrorContext(r.Context(), "media: photos: render gallery", "error", err)
	}
}

// handlePhotoGetError answers a failed item/photo lookup: 404 for
// storagedomain.ErrItemNotFound or domain.ErrPhotoNotFound (unknown or
// invisible — never distinguished, per the visibility contract every
// PhotoService method carries), logged 500 otherwise. Mirrors
// storage/adapter.ItemsWebHandlers.handleItemGetError's identical shape.
func (h *PhotosWebHandlers) handlePhotoGetError(w http.ResponseWriter, r *http.Request, err error, op string) {
	if errors.Is(err, storagedomain.ErrItemNotFound) || errors.Is(err, domain.ErrPhotoNotFound) {
		http.NotFound(w, r)
		return
	}
	h.logger.ErrorContext(r.Context(), op, "error", err)
	http.Error(w, errInternalServerError, http.StatusInternalServerError)
}

// verifyRequest parses r's application/x-www-form-urlencoded body and
// verifies its CSRF token — mirrors storage/adapter's own verifyRequest (a
// different package, so it cannot be reused directly here). Answers 400 or
// 403 and reports ok=false on failure.
func verifyRequest(w http.ResponseWriter, r *http.Request, sm *scs.SessionManager) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	if !session.VerifyCSRF(r, sm) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return false
	}
	return true
}

// verifyUploadRequest is verifyRequest's multipart/form-data equivalent —
// Upload's POST body carries file parts, which r.ParseForm alone never
// parses (it explicitly skips a multipart body), so this parses via
// r.ParseMultipartForm(multipartMaxMemory) instead, which also populates
// r.PostForm with the csrf_token field CSRF verification needs. Answers 400
// or 403 and reports ok=false on failure.
func verifyUploadRequest(w http.ResponseWriter, r *http.Request, sm *scs.SessionManager) bool {
	if err := r.ParseMultipartForm(multipartMaxMemory); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	if !session.VerifyCSRF(r, sm) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return false
	}
	return true
}

// mapPhotoError maps a domain error from a PhotoService call to the HTTP
// status and per-file/per-request message the gallery fragment carries. ok
// reports whether err was recognized; an unrecognized error is the
// caller's cue to log it and answer a generic 500 instead. Mapping fixed by
// Sprint 5 reconciliation R6: domain.ErrDuplicatePhoto never appears here —
// PhotoService.Upload resolves it internally and never returns it to a
// caller (see that method's own doc), so upload dedup is always silent.
func mapPhotoError(err error) (status int, message string, ok bool) {
	switch {
	case errors.Is(err, domain.ErrUnsupportedMediaType):
		return http.StatusUnsupportedMediaType, "Only JPEG and PNG photos are supported.", true
	case errors.Is(err, domain.ErrPhotoTooLarge):
		return http.StatusRequestEntityTooLarge, "That photo is too large.", true
	case errors.Is(err, domain.ErrInvalidPhoto):
		return http.StatusBadRequest, "That file could not be read as a photo.", true
	case errors.Is(err, domain.ErrPhotoNotFound):
		return http.StatusNotFound, "That photo no longer exists.", true
	case errors.Is(err, storagedomain.ErrItemNotFound):
		return http.StatusNotFound, "That item no longer exists.", true
	default:
		return 0, "", false
	}
}

// moveOrder returns current's photo ids with photoID swapped one position
// toward dir ("up" or "down"), the full order PhotoService.Reorder needs
// (it has no single-step primitive — see Move's own doc). A move already at
// its boundary (up on the first photo, down on the last) is a harmless
// no-op: the unchanged order is returned rather than an error, since
// photo_gallery.templ only ever renders the button that is not at a
// boundary, but a second, in-flight request racing a reorder could still
// reach here. Returns domain.ErrPhotoNotFound when photoID is not among
// current (defensive: ListForItem already scoped current to itemID), or a
// wrapped domain.ErrInvalidPhoto for an unrecognized dir.
func moveOrder(current []domain.ItemPhoto, photoID domain.PhotoID, dir string) ([]domain.PhotoID, error) {
	order := make([]domain.PhotoID, len(current))
	idx := -1
	for i, ip := range current {
		order[i] = ip.Photo.ID
		if ip.Photo.ID == photoID {
			idx = i
		}
	}
	if idx == -1 {
		return nil, domain.ErrPhotoNotFound
	}

	var swapWith int
	switch dir {
	case "up":
		swapWith = idx - 1
	case "down":
		swapWith = idx + 1
	default:
		return nil, fmt.Errorf("%w: unknown move direction %q", domain.ErrInvalidPhoto, dir)
	}
	if swapWith < 0 || swapWith >= len(order) {
		return order, nil
	}
	order[idx], order[swapWith] = order[swapWith], order[idx]
	return order, nil
}

// buildPhotoGalleryView projects itemID's []domain.ItemPhoto into
// PhotoGallerySection's view model, carrying errs (upload/mutation errors
// pending display) and a fresh CSRF token for every control the fragment
// renders.
func buildPhotoGalleryView(itemID storagedomain.ItemID, photos []domain.ItemPhoto, csrfToken string, errs []string) components.PhotoGalleryView {
	views := make([]components.PhotoView, 0, len(photos))
	for i, ip := range photos {
		views = append(views, components.PhotoView{
			ID:          ip.Photo.ID.String(),
			ThumbURL:    photoURL(itemID, ip.Photo.ID, domain.PhotoVariantThumb),
			FullURL:     photoURL(itemID, ip.Photo.ID, domain.PhotoVariantFull),
			IsPrimary:   ip.IsPrimary,
			CanMoveUp:   i > 0,
			CanMoveDown: i < len(photos)-1,
		})
	}
	return components.PhotoGalleryView{ItemID: itemID.String(), CSRFToken: csrfToken, Photos: views, Errors: errs}
}

// photoURL builds itemID/photoID's serve URL for variant — always this
// package's own Serve route (never a direct storage locator, see Serve's
// own doc for why): the gallery grid and the lightbox both point here,
// distinguished only by the size query parameter.
func photoURL(itemID storagedomain.ItemID, photoID domain.PhotoID, variant domain.PhotoVariant) string {
	return "/items/" + itemID.String() + "/photos/" + photoID.String() + "?size=" + variant.String()
}
