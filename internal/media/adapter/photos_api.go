package adapter

import (
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/media/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/api"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// apiUploadFramingOverhead bounds the extra bytes http.MaxBytesReader allows
// beyond maxUploadBytes for the surrounding multipart envelope (the
// boundary delimiters and the "photo" part's own Content-Disposition/
// Content-Type headers) — the uploaded file's own bytes are still capped at
// maxUploadBytes by PhotoValidator.ValidateAndStage (see
// PhotosAPIHandlers.Upload's own doc), so this only has to cover framing,
// never the payload itself.
const apiUploadFramingOverhead = 4 << 10 // 4 KiB

// photoFilePart names the multipart field PhotosAPIHandlers.Upload reads
// its file from — the API's own single-file contract, distinct from
// photos_web.go's photoFileField (the gallery's multi-file picker field
// name), since this ticket's own route accepts exactly one file per
// request.
const photoFilePart = "photo"

// PhotosAPIHandlers serves NSTR-56's public JSON/multipart API surface for
// item photos, delegating to the exact same photoOperator port
// PhotosWebHandlers already depends on (photos_web.go) — this ticket writes
// no application service, validation, scrubbing, or storage code of its
// own; reusing photoOperator (and mapPhotoError below) verbatim is what
// guarantees the EXIF-stripping and validation parity the ticket's own
// acceptance criteria demand. Every route is registered on the api.Router
// NSTR-53 mounts behind its bearer-only gate; this type carries no session
// and no CSRF dependency, unlike its web counterpart — API auth is the
// Authorization header alone (NSTR-53), which is what removes the CSRF
// surface, including on the serve route (the Android image loader sends the
// Authorization header itself, so no separate signed-URL scheme is needed).
type PhotosAPIHandlers struct {
	photos         photoOperator
	maxUploadBytes int64
	logger         *slog.Logger
}

// NewPhotosAPIHandlers constructs PhotosAPIHandlers. photos and logger are
// required and panic on nil, matching every other WebHandlers/APIHandlers
// constructor in this codebase. maxUploadBytes must be positive — it flows
// from the same cfg.Media.MaxUploadBytes value mediaapp.NewPhotoService
// itself already validates, so a composition root wiring both from that one
// config value never observes this panic in practice; it is a defensive
// assertion against a future caller wiring this type independently, not a
// distinct config validation path.
func NewPhotosAPIHandlers(photos photoOperator, maxUploadBytes int64, logger *slog.Logger) *PhotosAPIHandlers {
	if photos == nil {
		panic("media/adapter: NewPhotosAPIHandlers requires a non-nil photoOperator")
	}
	if maxUploadBytes <= 0 {
		panic("media/adapter: NewPhotosAPIHandlers requires a positive maxUploadBytes")
	}
	if logger == nil {
		panic("media/adapter: NewPhotosAPIHandlers requires a non-nil logger")
	}
	return &PhotosAPIHandlers{photos: photos, maxUploadBytes: maxUploadBytes, logger: logger}
}

// Routes registers the item photo routes on mux — an api.Registrar (DIP),
// see LocationsAPIHandlers.Routes' own doc for why. There is no reorder
// route: photo ordering (PhotosWebHandlers.Move) is a web-only gallery
// affordance, deliberately out of this ticket's scope.
func (h *PhotosAPIHandlers) Routes(mux api.Registrar) {
	mux.HandleFunc("GET /api/v1/items/{id}/photos", h.List)
	mux.HandleFunc("POST /api/v1/items/{id}/photos", h.Upload)
	mux.HandleFunc("GET /api/v1/items/{id}/photos/{photoID}", h.Serve)
	mux.HandleFunc("PUT /api/v1/items/{id}/photos/{photoID}/primary", h.SetPrimary)
	mux.HandleFunc("DELETE /api/v1/items/{id}/photos/{photoID}", h.Delete)
}

// photoResponse is every photo endpoint's response shape. URL/ThumbURL
// always point back at this adapter's own Serve route (app-served, stable,
// auth-scoped) — never a raw storage locator — mirroring photoURL's
// identical contract for the web gallery (photos_web.go); Serve itself is
// what redirects to a presigned URL when the backend supports one.
type photoResponse struct {
	ID          string `json:"id"`
	Primary     bool   `json:"primary"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	URL         string `json:"url"`
	ThumbURL    string `json:"thumb_url"`
}

// photoResponseFrom projects itemID/ip into its response DTO.
func photoResponseFrom(itemID storagedomain.ItemID, ip domain.ItemPhoto) photoResponse {
	full := apiPhotoURL(itemID, ip.Photo.ID)
	return photoResponse{
		ID:          ip.Photo.ID.String(),
		Primary:     ip.IsPrimary,
		ContentType: ip.Photo.ContentType,
		SizeBytes:   ip.Photo.SizeBytes,
		URL:         full,
		ThumbURL:    full + "?size=" + domain.PhotoVariantThumb.String(),
	}
}

// apiPhotoURL builds itemID/photoID's own Serve route — the same route this
// handler group itself registers, never a direct storage locator.
func apiPhotoURL(itemID storagedomain.ItemID, photoID domain.PhotoID) string {
	return "/api/v1/items/" + itemID.String() + "/photos/" + photoID.String()
}

// List handles GET /api/v1/items/{id}/photos: a bare JSON array (not
// wrapped in a pagination envelope — household scale means an item's own
// photo count never warrants one).
func (h *PhotosAPIHandlers) List(w http.ResponseWriter, r *http.Request) {
	itemID, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	photos, err := h.photos.ListForItem(r.Context(), viewer, itemID)
	if err != nil {
		h.writePhotoError(w, r, err, "media api: photos: list")
		return
	}
	out := make([]photoResponse, 0, len(photos))
	for _, ip := range photos {
		out = append(out, photoResponseFrom(itemID, ip))
	}
	api.WriteJSON(w, h.logger, http.StatusOK, out)
}

// Upload handles POST /api/v1/items/{id}/photos: a single-file streaming
// multipart upload under photoFilePart ("photo"), in the order the ticket's
// own implementation plan fixes:
//
//  1. A Content-Length that already declares more than the cap answers 413
//     before a single body byte is read.
//  2. The body is wrapped in http.MaxBytesReader at the cap plus
//     apiUploadFramingOverhead; any later read that trips it (a lying or
//     absent Content-Length) surfaces a *http.MaxBytesError, mapped to 413.
//  3. The body is read via Request.MultipartReader — never
//     ParseMultipartForm, which buffers to memory or a temp file net/http
//     itself manages — iterating parts until the "photo" file part is
//     found. No file part answers 422.
//
// PhotoValidator.ValidateAndStage (inside photoOperator.Upload) already caps
// its own read of the file part at maxUploadBytes+1, so in practice an
// oversized file is rejected there, as domain.ErrPhotoTooLarge (mapped by
// writePhotoError), before the MaxBytesReader's own larger cap is ever
// reached — MaxBytesReader is the backstop for the multipart envelope
// itself (excess headers/parts ahead of the file), not the usual path.
func (h *PhotosAPIHandlers) Upload(w http.ResponseWriter, r *http.Request) {
	itemID, ok := h.pathItemID(w, r)
	if !ok {
		return
	}

	maxTotalBytes := h.maxUploadBytes + apiUploadFramingOverhead
	if r.ContentLength > maxTotalBytes {
		h.writeUploadTooLarge(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTotalBytes)

	mr, err := r.MultipartReader()
	if err != nil {
		api.WriteError(w, h.logger, http.StatusBadRequest, api.CodeInvalidRequest, "malformed multipart request")
		return
	}
	part, err := findFilePart(mr, photoFilePart)
	if err != nil {
		h.writeUploadReadError(w, err)
		return
	}
	if part == nil {
		api.WriteError(w, h.logger, http.StatusUnprocessableEntity, api.CodeInvalidRequest, "no photo file part found",
			api.FieldDetail{Field: photoFilePart, Message: "required"})
		return
	}
	defer func() { _ = part.Close() }()

	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	photo, err := h.photos.Upload(r.Context(), viewer, itemID, part)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			h.writeUploadTooLarge(w)
			return
		}
		h.writePhotoError(w, r, err, "media api: photos: upload")
		return
	}

	// Reload rather than trust Upload's own *domain.Photo return, which
	// carries no Position/IsPrimary — the same "authorize-by-read, then
	// reload to build the response DTO" shape
	// storage/adapter.ItemsAPIHandlers.Update follows after its own Edit
	// call.
	items, err := h.photos.ListForItem(r.Context(), viewer, itemID)
	if err != nil {
		h.writePhotoError(w, r, err, "media api: photos: upload: reload")
		return
	}
	ip, found := findItemPhoto(items, photo.ID)
	if !found {
		h.logger.ErrorContext(r.Context(), "media api: photos: upload: uploaded photo missing from reload", "photo_id", photo.ID.String())
		api.WriteError(w, h.logger, http.StatusInternalServerError, api.CodeInternal, errInternalServerError)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusCreated, photoResponseFrom(itemID, ip))
}

// writeUploadTooLarge answers 413 for both of Upload's oversize branches: a
// Content-Length that already declares too much, and the MaxBytesReader
// backstop tripping during an actual read.
func (h *PhotosAPIHandlers) writeUploadTooLarge(w http.ResponseWriter) {
	api.WriteError(w, h.logger, http.StatusRequestEntityTooLarge, api.CodeInvalidRequest, "request body exceeds the maximum upload size")
}

// writeUploadReadError answers a multipart-parsing failure that is not
// itself an oversize body (a malformed boundary, a client that hung up
// mid-part) with 400 — a genuine oversize trip is a *http.MaxBytesError,
// which Upload checks for and routes to writeUploadTooLarge before ever
// reaching this function.
func (h *PhotosAPIHandlers) writeUploadReadError(w http.ResponseWriter, _ error) {
	api.WriteError(w, h.logger, http.StatusBadRequest, api.CodeInvalidRequest, "malformed multipart request")
}

// findFilePart scans mr for the first file part named field, closing and
// skipping any other part it passes along the way (an extra field a client
// happened to send is not itself an error). Returns nil, nil when the
// stream ends with no match — Upload's own "no file part" 422 case,
// deliberately distinct from a genuine read error, which the caller maps to
// 413/400 via writeUploadReadError/writeUploadTooLarge.
func findFilePart(mr *multipart.Reader, field string) (*multipart.Part, error) {
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if part.FormName() == field && part.FileName() != "" {
			return part, nil
		}
		_ = part.Close()
	}
}

// findItemPhoto returns id's ItemPhoto within photos (an itemID-scoped
// ListForItem result), or ok=false when no match exists.
func findItemPhoto(photos []domain.ItemPhoto, id domain.PhotoID) (ip domain.ItemPhoto, ok bool) {
	for _, candidate := range photos {
		if candidate.Photo.ID == id {
			return candidate, true
		}
	}
	return domain.ItemPhoto{}, false
}

// Serve handles GET /api/v1/items/{id}/photos/{photoID}: a 302 redirect to
// a presigned URL when the store supports one (S3), or the image bytes
// streamed through this process otherwise — the exact same branch shape as
// PhotosWebHandlers.Serve (see its own doc), over the same photoOperator, so
// visibility and variant-fallback behavior can never drift between the two
// surfaces.
func (h *PhotosAPIHandlers) Serve(w http.ResponseWriter, r *http.Request) {
	itemID, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	photoID, ok := h.pathPhotoID(w, r)
	if !ok {
		return
	}

	variant := domain.PhotoVariantFull
	if sizeParam := r.URL.Query().Get("size"); sizeParam != "" {
		v, err := domain.ParsePhotoVariant(sizeParam)
		if err != nil {
			api.WriteError(w, h.logger, http.StatusBadRequest, api.CodeInvalidRequest, "malformed request",
				api.FieldDetail{Field: "size", Message: `must be "full" or "thumb"`})
			return
		}
		variant = v
	}

	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	if h.photos.SupportsDirectURL() {
		url, err := h.photos.DirectURL(r.Context(), viewer, itemID, photoID, variant)
		if err != nil {
			h.writePhotoError(w, r, err, "media api: photos: direct url")
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
		return
	}

	rc, contentType, err := h.photos.Open(r.Context(), viewer, itemID, photoID, variant)
	if err != nil {
		h.writePhotoError(w, r, err, "media api: photos: open")
		return
	}
	defer func() { _ = rc.Close() }()
	w.Header().Set("Content-Type", contentType)
	if _, err := io.Copy(w, rc); err != nil {
		h.logger.ErrorContext(r.Context(), "media api: photos: stream", "error", err)
	}
}

// SetPrimary handles PUT /api/v1/items/{id}/photos/{photoID}/primary: 204
// with no response body on success — unlike NSTR-55's own transition
// endpoints, there is no updated projection worth returning here, since a
// photo carries no state a client would need to re-read after the call.
func (h *PhotosAPIHandlers) SetPrimary(w http.ResponseWriter, r *http.Request) {
	itemID, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	photoID, ok := h.pathPhotoID(w, r)
	if !ok {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	if err := h.photos.SetPrimary(r.Context(), viewer, itemID, photoID); err != nil {
		h.writePhotoError(w, r, err, "media api: photos: set primary")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Delete handles DELETE /api/v1/items/{id}/photos/{photoID}: 204 on
// success.
func (h *PhotosAPIHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	itemID, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	photoID, ok := h.pathPhotoID(w, r)
	if !ok {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	if err := h.photos.Delete(r.Context(), viewer, itemID, photoID); err != nil {
		h.writePhotoError(w, r, err, "media api: photos: delete")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pathItemID parses r's {id} path value, answering 400 invalid_request and
// reporting ok=false on a malformed one — the JSON-envelope analog of
// PhotosWebHandlers' own pathItemID (photos_web.go): storage/adapter's
// generic parsePathID (api_common.go) lives in a different package and
// cannot be reused directly here.
func (h *PhotosAPIHandlers) pathItemID(w http.ResponseWriter, r *http.Request) (storagedomain.ItemID, bool) {
	id, err := storagedomain.ParseItemID(r.PathValue("id"))
	if err != nil {
		api.WriteError(w, h.logger, http.StatusBadRequest, api.CodeInvalidRequest, "malformed id")
		return storagedomain.ItemID{}, false
	}
	return id, true
}

// pathPhotoID parses r's {photoID} path value, answering 400
// invalid_request and reporting ok=false on a malformed one.
func (h *PhotosAPIHandlers) pathPhotoID(w http.ResponseWriter, r *http.Request) (domain.PhotoID, bool) {
	id, err := domain.ParsePhotoID(r.PathValue("photoID"))
	if err != nil {
		api.WriteError(w, h.logger, http.StatusBadRequest, api.CodeInvalidRequest, "malformed photoID")
		return domain.PhotoID{}, false
	}
	return id, true
}

// writePhotoError maps err (from ListForItem/Open/DirectURL/SetPrimary/
// Delete/Upload) via mapPhotoError (photos_web.go) and writes the shared
// envelope — CodeNotFound for a 404, CodeInvalidRequest for anything else
// mapPhotoError recognizes (413/415/400 all describe a rejected request,
// and the api.Code vocabulary carries no more specific code for any of the
// three) — or logs and answers a generic 500 when err is unrecognized. The
// one place every handler in this type routes a photoOperator error
// through, mirroring storage/adapter's own writeDomainError.
func (h *PhotosAPIHandlers) writePhotoError(w http.ResponseWriter, r *http.Request, err error, op string) {
	status, message, ok := mapPhotoError(err)
	if !ok {
		h.logger.ErrorContext(r.Context(), op, "error", err)
		api.WriteError(w, h.logger, http.StatusInternalServerError, api.CodeInternal, errInternalServerError)
		return
	}
	code := api.CodeInvalidRequest
	if status == http.StatusNotFound {
		code = api.CodeNotFound
	}
	api.WriteError(w, h.logger, status, code, message)
}
