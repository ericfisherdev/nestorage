package adapter_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/media/adapter"
	"github.com/ericfisherdev/nestorage/internal/media/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/api"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// defaultTestMaxUploadBytes is the cap newAPIPrincipalServer's own
// PhotosAPIHandlers is constructed with unless a test overrides it via
// newPhotosAPIServerWithCap — generous enough that no ordinary test fixture
// (a handful of bytes of fake "photo" content) trips it by accident.
const defaultTestMaxUploadBytes = 1 << 20 // 1 MiB

// newAPIPrincipalServer starts an httptest.Server serving photos' routes
// (registered on the real api.Router, so the JSON 404/405 fallback and
// route-pattern population behave exactly like production) behind the real
// identityadapter.Resolve middleware, resolved to viewer on every request —
// the session-less analog of newPhotosWebHarness, mirroring
// storage/adapter's own identically-named fixture (api_common_test.go, a
// different package, so it cannot be reused directly here).
func newAPIPrincipalServer(t *testing.T, viewer identity.Principal, photos *adapter.PhotosAPIHandlers) *httptest.Server {
	t.Helper()
	apiRouter := api.NewRouter(testLogger())
	photos.Routes(apiRouter)

	chain := identityadapter.NewChain(fixedPrincipalResolver{principal: viewer}, absentCredentialResolver{}, absentCredentialResolver{})
	denier := identityadapter.NewDenier(testLogger())
	resolve := identityadapter.Resolve(chain, denier, testLogger())

	server := httptest.NewServer(resolve(apiRouter.Handler()))
	t.Cleanup(server.Close)
	return server
}

// newPhotosAPIServer builds a PhotosAPIHandlers over photos at
// defaultTestMaxUploadBytes and serves it through newAPIPrincipalServer.
func newPhotosAPIServer(t *testing.T, viewer identity.Principal, photos *fakePhotoOperator) *httptest.Server {
	t.Helper()
	return newAPIPrincipalServer(t, viewer, adapter.NewPhotosAPIHandlers(photos, defaultTestMaxUploadBytes, testLogger()))
}

// apiRequest issues method against server.URL+path with an optional JSON
// body, mirroring storage/adapter's own apiRequest (api_common_test.go, a
// different package). The caller is responsible for closing the returned
// response's body.
func apiRequest(t *testing.T, server *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest(%s %s): %v", method, path, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do(%s %s): %v", method, path, err)
	}
	return resp
}

func apiGet(t *testing.T, server *httptest.Server, path string) *http.Response {
	t.Helper()
	return apiRequest(t, server, http.MethodGet, path, "")
}

func apiPut(t *testing.T, server *httptest.Server, path string) *http.Response {
	t.Helper()
	return apiRequest(t, server, http.MethodPut, path, "")
}

func apiDelete(t *testing.T, server *httptest.Server, path string) *http.Response {
	t.Helper()
	return apiRequest(t, server, http.MethodDelete, path, "")
}

// assertErrorCode decodes resp's body as the shared envelope and asserts
// its error code, mirroring storage/adapter's own assertErrorCode.
func assertErrorCode(t *testing.T, resp *http.Response, wantCode string) {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if body.Error.Code != wantCode {
		t.Errorf("error code = %q, want %q", body.Error.Code, wantCode)
	}
}

// multipartUploadRequest builds a POST request against server carrying one
// "photo" file part named filename with content, optionally overriding the
// request's declared ContentLength — a negative override forces an unknown
// (chunked) length so the test exercises the lying-Content-Length path
// rather than Upload's own upfront precheck.
func multipartUploadRequest(t *testing.T, server *httptest.Server, path, filename, content string, contentLengthOverride int64) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("photo", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("multipart Close: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, server.URL+path, &buf)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if contentLengthOverride < 0 {
		req.ContentLength = -1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do POST %s: %v", path, err)
	}
	return resp
}

// --- NewPhotosAPIHandlers ------------------------------------------------
//
// Each dependency's panic is its own test, rather than a table over one
// shared field, because photoOperator is unexported to this package (media/
// adapter) — an external test (package adapter_test) can pass the literal
// nil for it but cannot name its type to declare a table field of that
// type, unlike PhotosWebHandlersDeps' own exported struct fields
// (photos_web_test.go's identical test).

func TestNewPhotosAPIHandlers_NilPhotoOperatorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewPhotosAPIHandlers(nil photoOperator) did not panic")
		}
	}()
	adapter.NewPhotosAPIHandlers(nil, defaultTestMaxUploadBytes, testLogger())
}

func TestNewPhotosAPIHandlers_NonPositiveMaxUploadBytesPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewPhotosAPIHandlers(maxUploadBytes <= 0) did not panic")
		}
	}()
	adapter.NewPhotosAPIHandlers(&fakePhotoOperator{}, 0, testLogger())
}

func TestNewPhotosAPIHandlers_NilLoggerPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewPhotosAPIHandlers(nil logger) did not panic")
		}
	}()
	adapter.NewPhotosAPIHandlers(&fakePhotoOperator{}, defaultTestMaxUploadBytes, nil)
}

// --- List ------------------------------------------------------------

func TestPhotosAPI_List_Success(t *testing.T) {
	itemID := storagedomain.NewItemID()
	photoID := domain.NewPhotoID()
	photos := &fakePhotoOperator{items: map[storagedomain.ItemID][]domain.ItemPhoto{
		itemID: {{Photo: domain.Photo{ID: photoID, ContentType: domain.ContentTypeJPEG, SizeBytes: 42}, Position: 0, IsPrimary: true}},
	}}
	server := newPhotosAPIServer(t, testViewer(), photos)

	resp := apiGet(t, server, "/api/v1/items/"+itemID.String()+"/photos")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("photos = %v, want exactly 1", got)
	}
	wantURL := "/api/v1/items/" + itemID.String() + "/photos/" + photoID.String()
	if got[0]["id"] != photoID.String() || got[0]["primary"] != true || got[0]["content_type"] != domain.ContentTypeJPEG {
		t.Errorf("photo = %+v, want id/primary/content_type reflecting the fixture", got[0])
	}
	if got[0]["url"] != wantURL || got[0]["thumb_url"] != wantURL+"?size=thumb" {
		t.Errorf("photo = %+v, want url/thumb_url pointing at this API's own serve route", got[0])
	}
}

func TestPhotosAPI_List_ItemNotFound(t *testing.T) {
	photos := &fakePhotoOperator{listErr: storagedomain.ErrItemNotFound}
	server := newPhotosAPIServer(t, testViewer(), photos)

	resp := apiGet(t, server, "/api/v1/items/"+storagedomain.NewItemID().String()+"/photos")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	assertErrorCode(t, resp, "not_found")
}

func TestPhotosAPI_List_UnmappedError_500(t *testing.T) {
	photos := &fakePhotoOperator{listErr: errors.New("boom")}
	server := newPhotosAPIServer(t, testViewer(), photos)

	resp := apiGet(t, server, "/api/v1/items/"+storagedomain.NewItemID().String()+"/photos")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
	assertErrorCode(t, resp, "internal")
}

func TestPhotosAPI_List_MalformedItemID_400(t *testing.T) {
	server := newPhotosAPIServer(t, testViewer(), &fakePhotoOperator{})
	resp := apiGet(t, server, "/api/v1/items/not-a-uuid/photos")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	assertErrorCode(t, resp, "invalid_request")
}

func TestPhotosAPI_List_IntegrationPrincipalAllowed(t *testing.T) {
	itemID := storagedomain.NewItemID()
	photos := &fakePhotoOperator{items: map[storagedomain.ItemID][]domain.ItemPhoto{itemID: {}}}
	server := newPhotosAPIServer(t, identity.NewIntegrationPrincipal("Nestova"), photos)

	resp := apiGet(t, server, "/api/v1/items/"+itemID.String()+"/photos")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (reads are open to both credential kinds)", resp.StatusCode)
	}
}

// --- Upload ------------------------------------------------------------

func TestPhotosAPI_Upload_Success_ReturnsPhotoDTO(t *testing.T) {
	itemID := storagedomain.NewItemID()
	photos := &fakePhotoOperator{}
	server := newPhotosAPIServer(t, testViewer(), photos)

	resp := multipartUploadRequest(t, server, "/api/v1/items/"+itemID.String()+"/photos", "a.jpg", "AAA", 0)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201:\n%s", resp.StatusCode, body)
	}
	if len(photos.uploadCalls) != 1 || photos.uploadCalls[0] != "AAA" {
		t.Errorf("uploadCalls = %v, want exactly [\"AAA\"]", photos.uploadCalls)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, body)
	}
	if got["id"] == nil || got["id"] == "" {
		t.Errorf("photo DTO missing id: %+v", got)
	}
}

func TestPhotosAPI_Upload_IntegrationPrincipalAllowed(t *testing.T) {
	// Unlike storage/adapter's create endpoints, uploading a photo has no
	// requireUserPrincipal gate — an integration principal (Nestova) may
	// attach photos too.
	itemID := storagedomain.NewItemID()
	photos := &fakePhotoOperator{}
	server := newPhotosAPIServer(t, identity.NewIntegrationPrincipal("Nestova"), photos)

	resp := multipartUploadRequest(t, server, "/api/v1/items/"+itemID.String()+"/photos", "a.jpg", "AAA", 0)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201:\n%s", resp.StatusCode, body)
	}
}

// oversizeUploadContent is well over apiUploadFramingOverhead (4 KiB) so
// even a tiny configured maxUploadBytes still leaves the WHOLE multipart
// request (boundary/header framing plus this content) larger than
// maxUploadBytes+the framing overhead — the margin
// TestPhotosAPI_Upload_ContentLengthPrecheck_413/
// TestPhotosAPI_Upload_LyingContentLength_413 both depend on to actually
// trip their respective 413 branch.
var oversizeUploadContent = strings.Repeat("x", 5000)

func TestPhotosAPI_Upload_ContentLengthPrecheck_413(t *testing.T) {
	itemID := storagedomain.NewItemID()
	photos := &fakePhotoOperator{}
	server := newAPIPrincipalServer(t, testViewer(), adapter.NewPhotosAPIHandlers(photos, 4, testLogger()))

	resp := multipartUploadRequest(t, server, "/api/v1/items/"+itemID.String()+"/photos", "a.jpg", oversizeUploadContent, 0)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	assertErrorCode(t, resp, "invalid_request")
	if len(photos.uploadCalls) != 0 {
		t.Error("Upload must not be called when the declared Content-Length already exceeds the cap")
	}
}

func TestPhotosAPI_Upload_LyingContentLength_413(t *testing.T) {
	// contentLengthOverride < 0 forces an unknown (chunked) declared
	// length, so the 413 below can only come from the MaxBytesReader
	// backstop tripping during the actual read — never from the upfront
	// Content-Length precheck (TestPhotosAPI_Upload_ContentLengthPrecheck_413
	// already covers that branch separately).
	itemID := storagedomain.NewItemID()
	photos := &fakePhotoOperator{}
	server := newAPIPrincipalServer(t, testViewer(), adapter.NewPhotosAPIHandlers(photos, 4, testLogger()))

	resp := multipartUploadRequest(t, server, "/api/v1/items/"+itemID.String()+"/photos", "a.jpg", oversizeUploadContent, -1)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	assertErrorCode(t, resp, "invalid_request")
	if len(photos.uploadCalls) != 0 {
		t.Error("Upload must not record a completed call when the body is rejected mid-stream")
	}
}

func TestPhotosAPI_Upload_NoFilePart_422(t *testing.T) {
	itemID := storagedomain.NewItemID()
	photos := &fakePhotoOperator{}
	server := newPhotosAPIServer(t, testViewer(), photos)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("not-the-photo-field", "x"); err != nil {
		t.Fatalf("WriteField: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/items/"+itemID.String()+"/photos", &buf)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	assertErrorCode(t, resp, "invalid_request")
}

func TestPhotosAPI_Upload_MalformedMultipart_400(t *testing.T) {
	itemID := storagedomain.NewItemID()
	server := newPhotosAPIServer(t, testViewer(), &fakePhotoOperator{})

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/items/"+itemID.String()+"/photos", strings.NewReader("not multipart at all"))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPhotosAPI_Upload_DomainErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"unsupported media type", domain.ErrUnsupportedMediaType, http.StatusUnsupportedMediaType, "invalid_request"},
		{"too large", domain.ErrPhotoTooLarge, http.StatusRequestEntityTooLarge, "invalid_request"},
		{"invalid photo", domain.ErrInvalidPhoto, http.StatusBadRequest, "invalid_request"},
		{"item not found", storagedomain.ErrItemNotFound, http.StatusNotFound, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			itemID := storagedomain.NewItemID()
			photos := &fakePhotoOperator{uploadErrsByContent: map[string]error{"BAD": tt.err}}
			server := newPhotosAPIServer(t, testViewer(), photos)

			resp := multipartUploadRequest(t, server, "/api/v1/items/"+itemID.String()+"/photos", "bad.jpg", "BAD", 0)
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			assertErrorCode(t, resp, tt.wantCode)
		})
	}
}

func TestPhotosAPI_Upload_UnmappedError_500(t *testing.T) {
	itemID := storagedomain.NewItemID()
	photos := &fakePhotoOperator{uploadErrsByContent: map[string]error{"BAD": errors.New("boom")}}
	server := newPhotosAPIServer(t, testViewer(), photos)

	resp := multipartUploadRequest(t, server, "/api/v1/items/"+itemID.String()+"/photos", "bad.jpg", "BAD", 0)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

func TestPhotosAPI_Upload_MalformedItemID_400(t *testing.T) {
	server := newPhotosAPIServer(t, testViewer(), &fakePhotoOperator{})
	resp := multipartUploadRequest(t, server, "/api/v1/items/not-a-uuid/photos", "a.jpg", "AAA", 0)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// --- Serve ------------------------------------------------------------

func TestPhotosAPI_Serve_LocalBackend_StreamsBytes(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{supportsDirect: false, openContentType: domain.ContentTypeJPEG}
	server := newPhotosAPIServer(t, testViewer(), photos)

	resp := apiGet(t, server, "/api/v1/items/"+itemID.String()+"/photos/"+photoID.String()+"?size=full")
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
}

func TestPhotosAPI_Serve_ThumbVariant(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{supportsDirect: false, openContentType: domain.ContentTypeJPEG}
	server := newPhotosAPIServer(t, testViewer(), photos)

	resp := apiGet(t, server, "/api/v1/items/"+itemID.String()+"/photos/"+photoID.String()+"?size=thumb")
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "bytes:thumb" {
		t.Errorf("body = %q, want the thumb variant's streamed bytes", body)
	}
	if len(photos.openCalls) != 1 || photos.openCalls[0] != domain.PhotoVariantThumb {
		t.Errorf("Open called with variants %v, want exactly [thumb]", photos.openCalls)
	}
}

func TestPhotosAPI_Serve_InvalidSizeParam_400(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	server := newPhotosAPIServer(t, testViewer(), &fakePhotoOperator{})

	resp := apiGet(t, server, "/api/v1/items/"+itemID.String()+"/photos/"+photoID.String()+"?size=huge")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	assertErrorCode(t, resp, "invalid_request")
}

func TestPhotosAPI_Serve_S3Backend_RedirectsToDirectURL(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{supportsDirect: true, directURL: "https://cdn.example.com/presigned"}
	server := newPhotosAPIServer(t, testViewer(), photos)

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(server.URL + "/api/v1/items/" + itemID.String() + "/photos/" + photoID.String())
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != photos.directURL {
		t.Errorf("Location = %q, want %q", loc, photos.directURL)
	}
	if len(photos.openCalls) != 0 {
		t.Error("S3 backend must never call Open")
	}
}

func TestPhotosAPI_Serve_NotFound(t *testing.T) {
	photos := &fakePhotoOperator{openErr: domain.ErrPhotoNotFound}
	server := newPhotosAPIServer(t, testViewer(), photos)

	resp := apiGet(t, server, "/api/v1/items/"+storagedomain.NewItemID().String()+"/photos/"+domain.NewPhotoID().String())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	assertErrorCode(t, resp, "not_found")
}

func TestPhotosAPI_Serve_MalformedIDs_400(t *testing.T) {
	server := newPhotosAPIServer(t, testViewer(), &fakePhotoOperator{})

	resp := apiGet(t, server, "/api/v1/items/not-a-uuid/photos/"+domain.NewPhotoID().String())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed item id: status = %d, want 400", resp.StatusCode)
	}

	resp2 := apiGet(t, server, "/api/v1/items/"+storagedomain.NewItemID().String()+"/photos/not-a-uuid")
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed photo id: status = %d, want 400", resp2.StatusCode)
	}
}

// --- SetPrimary ---------------------------------------------------------

func TestPhotosAPI_SetPrimary_Success(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{}
	server := newPhotosAPIServer(t, testViewer(), photos)

	resp := apiPut(t, server, "/api/v1/items/"+itemID.String()+"/photos/"+photoID.String()+"/primary")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if photos.setPrimaryCalls != 1 {
		t.Errorf("SetPrimary called %d times, want 1", photos.setPrimaryCalls)
	}
}

func TestPhotosAPI_SetPrimary_ErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"photo not found", domain.ErrPhotoNotFound, http.StatusNotFound, "not_found"},
		{"item not found", storagedomain.ErrItemNotFound, http.StatusNotFound, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
			photos := &fakePhotoOperator{setPrimaryErr: tt.err}
			server := newPhotosAPIServer(t, testViewer(), photos)

			resp := apiPut(t, server, "/api/v1/items/"+itemID.String()+"/photos/"+photoID.String()+"/primary")
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			assertErrorCode(t, resp, tt.wantCode)
		})
	}
}

func TestPhotosAPI_SetPrimary_UnmappedError_500(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{setPrimaryErr: errors.New("boom")}
	server := newPhotosAPIServer(t, testViewer(), photos)

	resp := apiPut(t, server, "/api/v1/items/"+itemID.String()+"/photos/"+photoID.String()+"/primary")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

func TestPhotosAPI_SetPrimary_MalformedIDs_400(t *testing.T) {
	server := newPhotosAPIServer(t, testViewer(), &fakePhotoOperator{})
	resp := apiPut(t, server, "/api/v1/items/not-a-uuid/photos/"+domain.NewPhotoID().String()+"/primary")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPhotosAPI_SetPrimary_IntegrationPrincipalAllowed(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{}
	server := newPhotosAPIServer(t, identity.NewIntegrationPrincipal("Nestova"), photos)

	resp := apiPut(t, server, "/api/v1/items/"+itemID.String()+"/photos/"+photoID.String()+"/primary")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}

// --- Delete ---------------------------------------------------------------

func TestPhotosAPI_Delete_Success(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{}
	server := newPhotosAPIServer(t, testViewer(), photos)

	resp := apiDelete(t, server, "/api/v1/items/"+itemID.String()+"/photos/"+photoID.String())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if photos.deleteCalls != 1 {
		t.Errorf("Delete called %d times, want 1", photos.deleteCalls)
	}
}

func TestPhotosAPI_Delete_NotFound(t *testing.T) {
	itemID, photoID := storagedomain.NewItemID(), domain.NewPhotoID()
	photos := &fakePhotoOperator{deleteErr: domain.ErrPhotoNotFound}
	server := newPhotosAPIServer(t, testViewer(), photos)

	resp := apiDelete(t, server, "/api/v1/items/"+itemID.String()+"/photos/"+photoID.String())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	assertErrorCode(t, resp, "not_found")
}

func TestPhotosAPI_Delete_MalformedIDs_400(t *testing.T) {
	server := newPhotosAPIServer(t, testViewer(), &fakePhotoOperator{})
	resp := apiDelete(t, server, "/api/v1/items/"+storagedomain.NewItemID().String()+"/photos/not-a-uuid")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
