package adapter_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/alexedwards/scs/v2"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/media/adapter"
	"github.com/ericfisherdev/nestorage/internal/media/domain"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// csrfRe extracts a rendered form's CSRF token, mirroring storage/adapter's
// own copy — a different, unexported-to-its-package instance, so this test
// package cannot reuse it directly.
var csrfRe = regexp.MustCompile(`name="csrf_token"\s+value="([^"]*)"`)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func testViewer() identity.Principal {
	return identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Viewer")
}

// fixedPrincipalResolver and absentCredentialResolver mirror
// storage/adapter's own identical fixtures (a different package, so neither
// can be reused directly here) — see that package's web_common_test.go for
// the full rationale.
type fixedPrincipalResolver struct{ principal identity.Principal }

func (f fixedPrincipalResolver) Resolve(_ context.Context, _ *http.Request) (identity.Principal, bool, error) {
	return f.principal, true, nil
}

type absentCredentialResolver struct{}

func (absentCredentialResolver) Resolve(_ context.Context, _ *http.Request) (identity.Principal, bool, error) {
	return identity.Principal{}, false, nil
}

func newPrincipalServer(t *testing.T, sm *scs.SessionManager, viewer identity.Principal, registerRoutes func(*http.ServeMux)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	registerRoutes(mux)

	chain := identityadapter.NewChain(fixedPrincipalResolver{principal: viewer}, absentCredentialResolver{}, absentCredentialResolver{})
	denier := identityadapter.NewDenier(testLogger())
	resolve := identityadapter.Resolve(chain, denier, testLogger())

	server := httptest.NewServer(sm.LoadAndSave(resolve(mux)))
	t.Cleanup(server.Close)
	return server
}

func newCSRFClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// fakePhotoOperator is a configurable photoOperator fake for
// PhotosWebHandlers' hermetic unit tests. Upload distinguishes calls by the
// uploaded content itself (read via io.ReadAll), since the port's own
// signature carries no filename — uploadErrsByContent lets a test configure
// a per-file outcome for a multi-file request.
type fakePhotoOperator struct {
	uploadErrsByContent map[string]error
	uploadCalls         []string

	items   map[storagedomain.ItemID][]domain.ItemPhoto
	listErr error
	// failListFromCall makes ListForItem start returning listErr only once
	// it has been called this many times (1-indexed) — 0 means "fail from
	// the first call". Lets a test seed a CSRF token through one working
	// List call before a later call (e.g. Move's own internal
	// ListForItem) starts failing.
	failListFromCall int
	listCalls        int

	openContentType string
	openErr         error
	openCalls       []domain.PhotoVariant

	directURL      string
	directErr      error
	directCalls    []domain.PhotoVariant
	supportsDirect bool

	setPrimaryErr   error
	setPrimaryCalls int

	reorderErr   error
	reorderCalls [][]domain.PhotoID

	deleteErr   error
	deleteCalls int
}

// Upload also attaches the resulting photo into f.items[itemID] — position
// len(existing), primary iff it is the item's first — mirroring
// mediaapp.PhotoService.Upload's own real attach-to-item step (photo_service.go)
// closely enough that a subsequent ListForItem call (photos_api.go's own
// Upload reload, or a test's own assertion) sees it, the same way the real
// service's shared tables would.
func (f *fakePhotoOperator) Upload(_ context.Context, _ identity.Principal, itemID storagedomain.ItemID, r io.Reader) (*domain.Photo, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	content := string(data)
	if uploadErr, ok := f.uploadErrsByContent[content]; ok {
		return nil, uploadErr
	}
	f.uploadCalls = append(f.uploadCalls, content)
	photo := domain.Photo{ID: domain.NewPhotoID()}
	if f.items == nil {
		f.items = map[storagedomain.ItemID][]domain.ItemPhoto{}
	}
	existing := f.items[itemID]
	f.items[itemID] = append(existing, domain.ItemPhoto{Photo: photo, Position: len(existing), IsPrimary: len(existing) == 0})
	return &photo, nil
}

func (f *fakePhotoOperator) ListForItem(_ context.Context, _ identity.Principal, itemID storagedomain.ItemID) ([]domain.ItemPhoto, error) {
	f.listCalls++
	if f.listErr != nil && (f.failListFromCall == 0 || f.listCalls >= f.failListFromCall) {
		return nil, f.listErr
	}
	return f.items[itemID], nil
}

func (f *fakePhotoOperator) Open(_ context.Context, _ identity.Principal, _ storagedomain.ItemID, _ domain.PhotoID, variant domain.PhotoVariant) (io.ReadCloser, string, error) {
	f.openCalls = append(f.openCalls, variant)
	if f.openErr != nil {
		return nil, "", f.openErr
	}
	return io.NopCloser(strings.NewReader("bytes:" + variant.String())), f.openContentType, nil
}

func (f *fakePhotoOperator) DirectURL(_ context.Context, _ identity.Principal, _ storagedomain.ItemID, _ domain.PhotoID, variant domain.PhotoVariant) (string, error) {
	f.directCalls = append(f.directCalls, variant)
	if f.directErr != nil {
		return "", f.directErr
	}
	return f.directURL, nil
}

func (f *fakePhotoOperator) SupportsDirectURL() bool { return f.supportsDirect }

func (f *fakePhotoOperator) Delete(_ context.Context, _ identity.Principal, _ storagedomain.ItemID, _ domain.PhotoID) error {
	f.deleteCalls++
	return f.deleteErr
}

func (f *fakePhotoOperator) SetPrimary(_ context.Context, _ identity.Principal, _ storagedomain.ItemID, _ domain.PhotoID) error {
	f.setPrimaryCalls++
	return f.setPrimaryErr
}

func (f *fakePhotoOperator) Reorder(_ context.Context, _ identity.Principal, _ storagedomain.ItemID, order []domain.PhotoID) error {
	f.reorderCalls = append(f.reorderCalls, order)
	return f.reorderErr
}

// photosWebHarness bundles a running PhotosWebHandlers server and a client
// carrying its session cookie across requests, mirroring
// storage/adapter's own *WebHarness shape.
type photosWebHarness struct {
	server *httptest.Server
	client *http.Client
	photos *fakePhotoOperator
}

func newPhotosWebHarness(t *testing.T, viewer identity.Principal, photos *fakePhotoOperator) *photosWebHarness {
	t.Helper()
	sm := scs.New()
	handlers := adapter.NewPhotosWebHandlers(adapter.PhotosWebHandlersDeps{Photos: photos, SM: sm, Logger: testLogger()})
	server := newPrincipalServer(t, sm, viewer, handlers.Routes)
	return &photosWebHarness{server: server, client: newCSRFClient(t), photos: photos}
}

// getCSRF fetches the gallery fragment for itemID and extracts its upload
// form's CSRF token — present regardless of whether the item has any
// photos yet.
func (h *photosWebHarness) getCSRF(t *testing.T, itemID storagedomain.ItemID) string {
	t.Helper()
	resp, err := h.client.Get(h.server.URL + "/items/" + itemID.String() + "/photos")
	if err != nil {
		t.Fatalf("GET .../photos: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	m := csrfRe.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no CSRF token in gallery fragment:\n%s", body)
	}
	return string(m[1])
}

func (h *photosWebHarness) postForm(t *testing.T, path string, form url.Values, htmx bool) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.server.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(body)
}

// uploadFile is one part of a multipart upload request built by
// (*photosWebHarness).upload.
type uploadFile struct {
	filename string
	content  string
}

// upload posts a multipart/form-data request to path carrying csrf (empty
// to omit the field entirely) and files under photoOperator's own
// photoFileField ("photos").
func (h *photosWebHarness) upload(t *testing.T, path, csrf string, files []uploadFile) (*http.Response, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if csrf != "" {
		if err := mw.WriteField("csrf_token", csrf); err != nil {
			t.Fatalf("WriteField csrf_token: %v", err)
		}
	}
	for _, f := range files {
		part, err := mw.CreateFormFile("photos", f.filename)
		if err != nil {
			t.Fatalf("CreateFormFile(%s): %v", f.filename, err)
		}
		if _, err := part.Write([]byte(f.content)); err != nil {
			t.Fatalf("write %s: %v", f.filename, err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("multipart Close: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, h.server.URL+path, &buf)
	if err != nil {
		t.Fatalf("NewRequest %s: %v", path, err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("HX-Request", "true")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(body)
}

func TestNewPhotosWebHandlers_NilDependenciesPanic(t *testing.T) {
	sm := scs.New()
	base := adapter.PhotosWebHandlersDeps{Photos: &fakePhotoOperator{}, SM: sm, Logger: testLogger()}

	tests := []struct {
		name   string
		mutate func(*adapter.PhotosWebHandlersDeps)
	}{
		{"nil photo operator", func(d *adapter.PhotosWebHandlersDeps) { d.Photos = nil }},
		{"nil session manager", func(d *adapter.PhotosWebHandlersDeps) { d.SM = nil }},
		{"nil logger", func(d *adapter.PhotosWebHandlersDeps) { d.Logger = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewPhotosWebHandlers(%s) did not panic", tt.name)
				}
			}()
			deps := base
			tt.mutate(&deps)
			adapter.NewPhotosWebHandlers(deps)
		})
	}
}

// --- List ------------------------------------------------------------

func TestPhotosWebHandlers_List_Success_RendersPhotos(t *testing.T) {
	itemID := storagedomain.NewItemID()
	photoID := domain.NewPhotoID()
	photos := &fakePhotoOperator{items: map[storagedomain.ItemID][]domain.ItemPhoto{
		itemID: {{Photo: domain.Photo{ID: photoID}, Position: 0, IsPrimary: true}},
	}}
	h := newPhotosWebHarness(t, testViewer(), photos)

	resp, err := h.client.Get(h.server.URL + "/items/" + itemID.String() + "/photos")
	if err != nil {
		t.Fatalf("GET .../photos: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `id="item-photos"`) {
		t.Errorf("response missing the gallery section fragment: %s", body)
	}
	wantThumb := "/items/" + itemID.String() + "/photos/" + photoID.String() + "?size=thumb"
	if !strings.Contains(string(body), wantThumb) {
		t.Errorf("response missing thumbnail URL %q: %s", wantThumb, body)
	}
}

func TestPhotosWebHandlers_List_ItemNotFound(t *testing.T) {
	photos := &fakePhotoOperator{listErr: storagedomain.ErrItemNotFound}
	h := newPhotosWebHarness(t, testViewer(), photos)

	resp, err := h.client.Get(h.server.URL + "/items/" + storagedomain.NewItemID().String() + "/photos")
	if err != nil {
		t.Fatalf("GET .../photos: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestPhotosWebHandlers_List_UnmappedError_500(t *testing.T) {
	photos := &fakePhotoOperator{listErr: errors.New("boom")}
	h := newPhotosWebHarness(t, testViewer(), photos)

	resp, err := h.client.Get(h.server.URL + "/items/" + storagedomain.NewItemID().String() + "/photos")
	if err != nil {
		t.Fatalf("GET .../photos: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

func TestPhotosWebHandlers_List_MalformedItemID_400(t *testing.T) {
	h := newPhotosWebHarness(t, testViewer(), &fakePhotoOperator{})
	resp, err := h.client.Get(h.server.URL + "/items/not-a-uuid/photos")
	if err != nil {
		t.Fatalf("GET .../photos: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// --- Upload ------------------------------------------------------------

func TestPhotosWebHandlers_Upload_SingleFile_Success(t *testing.T) {
	itemID := storagedomain.NewItemID()
	photos := &fakePhotoOperator{}
	h := newPhotosWebHarness(t, testViewer(), photos)
	csrf := h.getCSRF(t, itemID)

	resp, body := h.upload(t, "/items/"+itemID.String()+"/photos", csrf, []uploadFile{{filename: "a.jpg", content: "AAA"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if len(photos.uploadCalls) != 1 || photos.uploadCalls[0] != "AAA" {
		t.Errorf("uploadCalls = %v, want exactly [\"AAA\"]", photos.uploadCalls)
	}
	if !strings.Contains(body, `id="item-photos"`) {
		t.Errorf("response did not re-render the gallery fragment: %s", body)
	}
}

func TestPhotosWebHandlers_Upload_MultiFile_MixedSuccessAndFailure_Returns200WithErrors(t *testing.T) {
	itemID := storagedomain.NewItemID()
	photos := &fakePhotoOperator{uploadErrsByContent: map[string]error{"BAD": domain.ErrUnsupportedMediaType}}
	h := newPhotosWebHarness(t, testViewer(), photos)
	csrf := h.getCSRF(t, itemID)

	resp, body := h.upload(t, "/items/"+itemID.String()+"/photos", csrf, []uploadFile{
		{filename: "good.jpg", content: "GOOD"},
		{filename: "bad.gif", content: "BAD"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (at least one file succeeded):\n%s", resp.StatusCode, body)
	}
	if len(photos.uploadCalls) != 1 || photos.uploadCalls[0] != "GOOD" {
		t.Errorf("uploadCalls = %v, want exactly [\"GOOD\"]", photos.uploadCalls)
	}
	if !strings.Contains(body, "bad.gif") || !strings.Contains(body, "Only JPEG and PNG photos are supported.") {
		t.Errorf("response missing the per-file rejection for bad.gif: %s", body)
	}
}

func TestPhotosWebHandlers_Upload_AllFilesFail_Returns422(t *testing.T) {
	itemID := storagedomain.NewItemID()
	photos := &fakePhotoOperator{uploadErrsByContent: map[string]error{"BAD": domain.ErrPhotoTooLarge}}
	h := newPhotosWebHarness(t, testViewer(), photos)
	csrf := h.getCSRF(t, itemID)

	resp, body := h.upload(t, "/items/"+itemID.String()+"/photos", csrf, []uploadFile{{filename: "bad.jpg", content: "BAD"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (every file failed):\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "That photo is too large.") {
		t.Errorf("response missing the mapped message: %s", body)
	}
}

func TestPhotosWebHandlers_Upload_UnmappedFileError_LoggedAsGenericMessage(t *testing.T) {
	itemID := storagedomain.NewItemID()
	photos := &fakePhotoOperator{uploadErrsByContent: map[string]error{"BAD": errors.New("boom")}}
	h := newPhotosWebHarness(t, testViewer(), photos)
	csrf := h.getCSRF(t, itemID)

	resp, body := h.upload(t, "/items/"+itemID.String()+"/photos", csrf, []uploadFile{{filename: "bad.jpg", content: "BAD"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "internal server error") {
		t.Errorf("response missing the generic fallback message for an unmapped per-file error: %s", body)
	}
}

func TestPhotosWebHandlers_Upload_NoFiles_Returns422(t *testing.T) {
	itemID := storagedomain.NewItemID()
	photos := &fakePhotoOperator{}
	h := newPhotosWebHarness(t, testViewer(), photos)
	csrf := h.getCSRF(t, itemID)

	resp, body := h.upload(t, "/items/"+itemID.String()+"/photos", csrf, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Please choose at least one photo.") {
		t.Errorf("response missing the no-files message: %s", body)
	}
}

func TestPhotosWebHandlers_Upload_CSRFRejected(t *testing.T) {
	itemID := storagedomain.NewItemID()
	photos := &fakePhotoOperator{}
	h := newPhotosWebHarness(t, testViewer(), photos)
	h.getCSRF(t, itemID) // establishes the session cookie

	resp, _ := h.upload(t, "/items/"+itemID.String()+"/photos", "wrong-token", []uploadFile{{filename: "a.jpg", content: "AAA"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	if len(photos.uploadCalls) != 0 {
		t.Error("Upload must not be called when CSRF verification fails")
	}
}

func TestPhotosWebHandlers_Upload_MalformedItemID_400(t *testing.T) {
	photos := &fakePhotoOperator{}
	h := newPhotosWebHarness(t, testViewer(), photos)
	resp, _ := h.upload(t, "/items/not-a-uuid/photos", "x", []uploadFile{{filename: "a.jpg", content: "AAA"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// --- Serve ------------------------------------------------------------

func TestPhotosWebHandlers_Serve_LocalBackend_StreamsBytes(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{supportsDirect: false, openContentType: domain.ContentTypeJPEG}
	h := newPhotosWebHarness(t, testViewer(), photos)

	resp, err := h.client.Get(h.server.URL + "/items/" + itemID.String() + "/photos/" + photoID.String() + "?size=full")
	if err != nil {
		t.Fatalf("GET .../photos/{id}: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != domain.ContentTypeJPEG {
		t.Errorf("Content-Type = %q, want %q", ct, domain.ContentTypeJPEG)
	}
	if string(body) != "bytes:full" {
		t.Errorf("body = %q, want streamed bytes for the full variant", body)
	}
	if len(photos.directCalls) != 0 {
		t.Error("local backend must never call DirectURL (it is a non-navigable locator, see Serve's own doc)")
	}
}

func TestPhotosWebHandlers_Serve_ThumbVariant_DefaultsCorrectly(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{supportsDirect: false, openContentType: domain.ContentTypeJPEG}
	h := newPhotosWebHarness(t, testViewer(), photos)

	resp, err := h.client.Get(h.server.URL + "/items/" + itemID.String() + "/photos/" + photoID.String() + "?size=thumb")
	if err != nil {
		t.Fatalf("GET .../photos/{id}?size=thumb: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "bytes:thumb" {
		t.Errorf("body = %q, want the thumb variant's streamed bytes", body)
	}
	if len(photos.openCalls) != 1 || photos.openCalls[0] != domain.PhotoVariantThumb {
		t.Errorf("Open called with variants %v, want exactly [thumb]", photos.openCalls)
	}

	// No size query parameter at all defaults to full — verified via a
	// second request against the same handler.
	resp2, err := h.client.Get(h.server.URL + "/items/" + itemID.String() + "/photos/" + photoID.String())
	if err != nil {
		t.Fatalf("GET .../photos/{id} (no size): %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if len(photos.openCalls) != 2 || photos.openCalls[1] != domain.PhotoVariantFull {
		t.Errorf("Open called with variants %v, want [thumb full]", photos.openCalls)
	}
}

func TestPhotosWebHandlers_Serve_InvalidSizeParam_400(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	h := newPhotosWebHarness(t, testViewer(), &fakePhotoOperator{})

	resp, err := h.client.Get(h.server.URL + "/items/" + itemID.String() + "/photos/" + photoID.String() + "?size=huge")
	if err != nil {
		t.Fatalf("GET .../photos/{id}?size=huge: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPhotosWebHandlers_Serve_S3Backend_RedirectsToDirectURL(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{supportsDirect: true, directURL: "https://cdn.example.com/presigned"}
	h := newPhotosWebHarness(t, testViewer(), photos)

	resp, err := h.client.Get(h.server.URL + "/items/" + itemID.String() + "/photos/" + photoID.String())
	if err != nil {
		t.Fatalf("GET .../photos/{id}: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != photos.directURL {
		t.Errorf("Location = %q, want %q", loc, photos.directURL)
	}
	if len(photos.openCalls) != 0 {
		t.Error("S3 backend must never call Open (it streams through this process only for the local backend)")
	}
}

func TestPhotosWebHandlers_Serve_NotFound(t *testing.T) {
	photos := &fakePhotoOperator{openErr: domain.ErrPhotoNotFound}
	h := newPhotosWebHarness(t, testViewer(), photos)

	resp, err := h.client.Get(h.server.URL + "/items/" + storagedomain.NewItemID().String() + "/photos/" + domain.NewPhotoID().String())
	if err != nil {
		t.Fatalf("GET .../photos/{id}: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestPhotosWebHandlers_Serve_UnmappedError_500(t *testing.T) {
	photos := &fakePhotoOperator{openErr: errors.New("boom")}
	h := newPhotosWebHarness(t, testViewer(), photos)

	resp, err := h.client.Get(h.server.URL + "/items/" + storagedomain.NewItemID().String() + "/photos/" + domain.NewPhotoID().String())
	if err != nil {
		t.Fatalf("GET .../photos/{id}: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

func TestPhotosWebHandlers_Serve_MalformedPhotoID_400(t *testing.T) {
	h := newPhotosWebHarness(t, testViewer(), &fakePhotoOperator{})
	resp, err := h.client.Get(h.server.URL + "/items/" + storagedomain.NewItemID().String() + "/photos/not-a-uuid")
	if err != nil {
		t.Fatalf("GET .../photos/not-a-uuid: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// --- SetPrimary ---------------------------------------------------------

func TestPhotosWebHandlers_SetPrimary_Success(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{}
	h := newPhotosWebHarness(t, testViewer(), photos)
	csrf := h.getCSRF(t, itemID)

	resp, body := h.postForm(t, "/items/"+itemID.String()+"/photos/"+photoID.String()+"/primary", url.Values{"csrf_token": {csrf}}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if photos.setPrimaryCalls != 1 {
		t.Errorf("SetPrimary called %d times, want 1", photos.setPrimaryCalls)
	}
	if !strings.Contains(body, `id="item-photos"`) {
		t.Errorf("response did not re-render the gallery fragment: %s", body)
	}
}

func TestPhotosWebHandlers_SetPrimary_ErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{"unsupported media type", domain.ErrUnsupportedMediaType, http.StatusUnsupportedMediaType, "Only JPEG and PNG photos are supported."},
		{"too large", domain.ErrPhotoTooLarge, http.StatusRequestEntityTooLarge, "That photo is too large."},
		{"invalid photo", domain.ErrInvalidPhoto, http.StatusBadRequest, "That file could not be read as a photo."},
		{"photo not found", domain.ErrPhotoNotFound, http.StatusNotFound, "That photo no longer exists."},
		{"item not found", storagedomain.ErrItemNotFound, http.StatusNotFound, "That item no longer exists."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
			photos := &fakePhotoOperator{setPrimaryErr: tt.err}
			h := newPhotosWebHarness(t, testViewer(), photos)
			csrf := h.getCSRF(t, itemID)

			resp, body := h.postForm(t, "/items/"+itemID.String()+"/photos/"+photoID.String()+"/primary", url.Values{"csrf_token": {csrf}}, true)
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d:\n%s", resp.StatusCode, tt.wantStatus, body)
			}
			if !strings.Contains(body, tt.wantMsg) {
				t.Errorf("body missing mapped message %q: %s", tt.wantMsg, body)
			}
		})
	}
}

func TestPhotosWebHandlers_SetPrimary_UnmappedError_500(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{setPrimaryErr: errors.New("boom")}
	h := newPhotosWebHarness(t, testViewer(), photos)
	csrf := h.getCSRF(t, itemID)

	resp, _ := h.postForm(t, "/items/"+itemID.String()+"/photos/"+photoID.String()+"/primary", url.Values{"csrf_token": {csrf}}, true)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

func TestPhotosWebHandlers_SetPrimary_CSRFRejected(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{}
	h := newPhotosWebHarness(t, testViewer(), photos)
	h.getCSRF(t, itemID)

	resp, _ := h.postForm(t, "/items/"+itemID.String()+"/photos/"+photoID.String()+"/primary", url.Values{"csrf_token": {"wrong-token"}}, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	if photos.setPrimaryCalls != 0 {
		t.Error("SetPrimary must not be called when CSRF verification fails")
	}
}

func TestPhotosWebHandlers_SetPrimary_MalformedPhotoID_400(t *testing.T) {
	itemID := storagedomain.NewItemID()
	h := newPhotosWebHarness(t, testViewer(), &fakePhotoOperator{})
	resp, _ := h.postForm(t, "/items/"+itemID.String()+"/photos/not-a-uuid/primary", url.Values{"csrf_token": {"x"}}, false)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// --- Move ---------------------------------------------------------------

func TestPhotosWebHandlers_Move_Up_SwapsWithPrecedingPhoto(t *testing.T) {
	itemID := storagedomain.NewItemID()
	p1, p2, p3 := domain.NewPhotoID(), domain.NewPhotoID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{items: map[storagedomain.ItemID][]domain.ItemPhoto{
		itemID: {{Photo: domain.Photo{ID: p1}}, {Photo: domain.Photo{ID: p2}}, {Photo: domain.Photo{ID: p3}}},
	}}
	h := newPhotosWebHarness(t, testViewer(), photos)
	csrf := h.getCSRF(t, itemID)

	resp, body := h.postForm(t, "/items/"+itemID.String()+"/photos/"+p2.String()+"/move", url.Values{"csrf_token": {csrf}, "dir": {"up"}}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if len(photos.reorderCalls) != 1 {
		t.Fatalf("Reorder called %d times, want 1", len(photos.reorderCalls))
	}
	want := []domain.PhotoID{p2, p1, p3}
	got := photos.reorderCalls[0]
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("Reorder order = %v, want %v (p2 swapped ahead of p1)", got, want)
	}
}

func TestPhotosWebHandlers_Move_Down_SwapsWithFollowingPhoto(t *testing.T) {
	itemID := storagedomain.NewItemID()
	p1, p2 := domain.NewPhotoID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{items: map[storagedomain.ItemID][]domain.ItemPhoto{
		itemID: {{Photo: domain.Photo{ID: p1}}, {Photo: domain.Photo{ID: p2}}},
	}}
	h := newPhotosWebHarness(t, testViewer(), photos)
	csrf := h.getCSRF(t, itemID)

	resp, body := h.postForm(t, "/items/"+itemID.String()+"/photos/"+p1.String()+"/move", url.Values{"csrf_token": {csrf}, "dir": {"down"}}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	want := []domain.PhotoID{p2, p1}
	got := photos.reorderCalls[0]
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Reorder order = %v, want %v", got, want)
	}
}

func TestPhotosWebHandlers_Move_AtBoundary_NoOpOrderStillReordered(t *testing.T) {
	itemID := storagedomain.NewItemID()
	p1, p2 := domain.NewPhotoID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{items: map[storagedomain.ItemID][]domain.ItemPhoto{
		itemID: {{Photo: domain.Photo{ID: p1}}, {Photo: domain.Photo{ID: p2}}},
	}}
	h := newPhotosWebHarness(t, testViewer(), photos)
	csrf := h.getCSRF(t, itemID)

	// p1 is already first: moving it "up" is a no-op, not an error.
	resp, body := h.postForm(t, "/items/"+itemID.String()+"/photos/"+p1.String()+"/move", url.Values{"csrf_token": {csrf}, "dir": {"up"}}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (boundary move is a harmless no-op):\n%s", resp.StatusCode, body)
	}
	want := []domain.PhotoID{p1, p2}
	got := photos.reorderCalls[0]
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Reorder order = %v, want unchanged %v", got, want)
	}
}

func TestPhotosWebHandlers_Move_UnknownDirection_Rejected(t *testing.T) {
	itemID := storagedomain.NewItemID()
	p1 := domain.NewPhotoID()
	photos := &fakePhotoOperator{items: map[storagedomain.ItemID][]domain.ItemPhoto{itemID: {{Photo: domain.Photo{ID: p1}}}}}
	h := newPhotosWebHarness(t, testViewer(), photos)
	csrf := h.getCSRF(t, itemID)

	resp, body := h.postForm(t, "/items/"+itemID.String()+"/photos/"+p1.String()+"/move", url.Values{"csrf_token": {csrf}, "dir": {"sideways"}}, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400:\n%s", resp.StatusCode, body)
	}
	if len(photos.reorderCalls) != 0 {
		t.Error("Reorder must not be called for an unrecognized direction")
	}
}

func TestPhotosWebHandlers_Move_PhotoNotAttached_404(t *testing.T) {
	itemID := storagedomain.NewItemID()
	photos := &fakePhotoOperator{items: map[storagedomain.ItemID][]domain.ItemPhoto{itemID: {}}}
	h := newPhotosWebHarness(t, testViewer(), photos)
	csrf := h.getCSRF(t, itemID)

	resp, body := h.postForm(t, "/items/"+itemID.String()+"/photos/"+domain.NewPhotoID().String()+"/move", url.Values{"csrf_token": {csrf}, "dir": {"up"}}, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404:\n%s", resp.StatusCode, body)
	}
}

// TestPhotosWebHandlers_Move_ListError_Propagates proves Move's own
// preceding ListForItem call (needed to compute the new order — see
// adapter.moveOrder's own doc) maps its error through the same
// renderMutationError tail every other failure branch uses, rather than
// leaking an unhandled error. failListFromCall lets the FIRST ListForItem
// call (List, minting the CSRF token below) succeed while the SECOND
// (Move's own internal call) fails.
func TestPhotosWebHandlers_Move_ListError_Propagates(t *testing.T) {
	itemID := storagedomain.NewItemID()
	photos := &fakePhotoOperator{
		items:            map[storagedomain.ItemID][]domain.ItemPhoto{itemID: {{Photo: domain.Photo{ID: domain.NewPhotoID()}}}},
		listErr:          storagedomain.ErrItemNotFound,
		failListFromCall: 2,
	}
	h := newPhotosWebHarness(t, testViewer(), photos)
	csrf := h.getCSRF(t, itemID)

	resp, body := h.postForm(t, "/items/"+itemID.String()+"/photos/"+domain.NewPhotoID().String()+"/move", url.Values{"csrf_token": {csrf}, "dir": {"up"}}, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (ListForItem's own ErrItemNotFound propagating):\n%s", resp.StatusCode, body)
	}
}

func TestPhotosWebHandlers_Move_UnmappedListError_500(t *testing.T) {
	itemID := storagedomain.NewItemID()
	photos := &fakePhotoOperator{
		items:            map[storagedomain.ItemID][]domain.ItemPhoto{itemID: {{Photo: domain.Photo{ID: domain.NewPhotoID()}}}},
		listErr:          errors.New("boom"),
		failListFromCall: 2,
	}
	h := newPhotosWebHarness(t, testViewer(), photos)
	csrf := h.getCSRF(t, itemID)

	resp, _ := h.postForm(t, "/items/"+itemID.String()+"/photos/"+domain.NewPhotoID().String()+"/move", url.Values{"csrf_token": {csrf}, "dir": {"up"}}, true)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

func TestPhotosWebHandlers_Move_UnmappedReorderError_500(t *testing.T) {
	itemID := storagedomain.NewItemID()
	p1, p2 := domain.NewPhotoID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{
		items:      map[storagedomain.ItemID][]domain.ItemPhoto{itemID: {{Photo: domain.Photo{ID: p1}}, {Photo: domain.Photo{ID: p2}}}},
		reorderErr: errors.New("boom"),
	}
	h := newPhotosWebHarness(t, testViewer(), photos)
	csrf := h.getCSRF(t, itemID)

	resp, _ := h.postForm(t, "/items/"+itemID.String()+"/photos/"+p1.String()+"/move", url.Values{"csrf_token": {csrf}, "dir": {"down"}}, true)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

func TestPhotosWebHandlers_Move_CSRFRejected(t *testing.T) {
	itemID := storagedomain.NewItemID()
	p1 := domain.NewPhotoID()
	photos := &fakePhotoOperator{items: map[storagedomain.ItemID][]domain.ItemPhoto{itemID: {{Photo: domain.Photo{ID: p1}}}}}
	h := newPhotosWebHarness(t, testViewer(), photos)
	h.getCSRF(t, itemID)

	resp, _ := h.postForm(t, "/items/"+itemID.String()+"/photos/"+p1.String()+"/move", url.Values{"csrf_token": {"wrong-token"}, "dir": {"up"}}, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	if len(photos.reorderCalls) != 0 {
		t.Error("Reorder must not be called when CSRF verification fails")
	}
}

// --- Delete ---------------------------------------------------------------

func TestPhotosWebHandlers_Delete_Success(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{}
	h := newPhotosWebHarness(t, testViewer(), photos)
	csrf := h.getCSRF(t, itemID)

	resp, body := h.postForm(t, "/items/"+itemID.String()+"/photos/"+photoID.String()+"/delete", url.Values{"csrf_token": {csrf}}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if photos.deleteCalls != 1 {
		t.Errorf("Delete called %d times, want 1", photos.deleteCalls)
	}
}

func TestPhotosWebHandlers_Delete_NotFound(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{deleteErr: domain.ErrPhotoNotFound}
	h := newPhotosWebHarness(t, testViewer(), photos)
	csrf := h.getCSRF(t, itemID)

	resp, body := h.postForm(t, "/items/"+itemID.String()+"/photos/"+photoID.String()+"/delete", url.Values{"csrf_token": {csrf}}, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404:\n%s", resp.StatusCode, body)
	}
}

func TestPhotosWebHandlers_Delete_CSRFRejected(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{}
	h := newPhotosWebHarness(t, testViewer(), photos)
	h.getCSRF(t, itemID)

	resp, _ := h.postForm(t, "/items/"+itemID.String()+"/photos/"+photoID.String()+"/delete", url.Values{"csrf_token": {"wrong-token"}}, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	if photos.deleteCalls != 0 {
		t.Error("Delete must not be called when CSRF verification fails")
	}
}

func TestPhotosWebHandlers_Delete_MalformedPhotoID_400(t *testing.T) {
	itemID := storagedomain.NewItemID()
	h := newPhotosWebHarness(t, testViewer(), &fakePhotoOperator{})
	resp, _ := h.postForm(t, "/items/"+itemID.String()+"/photos/not-a-uuid/delete", url.Values{"csrf_token": {"x"}}, false)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
