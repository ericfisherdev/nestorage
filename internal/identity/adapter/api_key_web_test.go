package adapter_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// fakeAPIKeyService is a configurable apiKeyWebService fake for
// APIKeyWebHandlers' hermetic unit tests. Each *Err field lets a test force
// the matching method to fail with a specific error, mirroring
// fakeAdminService's own shape.
type fakeAPIKeyService struct {
	current    *domain.APIKey
	hasCurrent bool
	list       []*domain.APIKey

	createErr error
	rotateErr error
	revokeErr error

	createCalls int
	rotateCalls int
	revokeCalls int
}

func (f *fakeAPIKeyService) Current(_ context.Context) (*domain.APIKey, bool, error) {
	return f.current, f.hasCurrent, nil
}

func (f *fakeAPIKeyService) List(_ context.Context) ([]*domain.APIKey, error) {
	return f.list, nil
}

func (f *fakeAPIKeyService) Create(_ context.Context, label string) (*domain.APIKey, string, error) {
	f.createCalls++
	if f.createErr != nil {
		return nil, "", f.createErr
	}
	k := &domain.APIKey{ID: domain.NewAPIKeyID(), KeyPrefix: "ns_aaaaaaaa", Label: label, CreatedAt: time.Now()}
	f.current = k
	f.hasCurrent = true
	f.list = append([]*domain.APIKey{k}, f.list...)
	return k, domain.APIKeyPrefix + strings.Repeat("a", 64), nil
}

func (f *fakeAPIKeyService) Rotate(_ context.Context, label string, _ domain.OverlapWindow) (*domain.APIKey, string, error) {
	f.rotateCalls++
	if f.rotateErr != nil {
		return nil, "", f.rotateErr
	}
	k := &domain.APIKey{ID: domain.NewAPIKeyID(), KeyPrefix: "ns_bbbbbbbb", Label: label, CreatedAt: time.Now()}
	f.current = k
	f.hasCurrent = true
	f.list = append([]*domain.APIKey{k}, f.list...)
	return k, domain.APIKeyPrefix + strings.Repeat("b", 64), nil
}

func (f *fakeAPIKeyService) Revoke(_ context.Context, _ domain.APIKeyID) error {
	f.revokeCalls++
	return f.revokeErr
}

type apiKeyWebHarness struct {
	server *httptest.Server
	client *http.Client
	keys   *fakeAPIKeyService
}

// newAPIKeyWebHarness wires APIKeyWebHandlers over an httptest server,
// reusing testUsersLayout/testLogger (package-level in this test package)
// rather than redefining an equivalent fixture.
func newAPIKeyWebHarness(t *testing.T, keys *fakeAPIKeyService) *apiKeyWebHarness {
	t.Helper()
	sm := scs.New()
	handlers := adapter.NewAPIKeyWebHandlers(keys, sm, testUsersLayout, testLogger())

	mux := http.NewServeMux()
	handlers.Routes(mux)
	server := httptest.NewServer(sm.LoadAndSave(mux))
	t.Cleanup(server.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &apiKeyWebHarness{server: server, client: client, keys: keys}
}

// getCSRF performs a GET against /settings/api-key and returns the embedded
// CSRF token, failing the test if the form (or its token) is missing.
func (h *apiKeyWebHarness) getCSRF(t *testing.T) string {
	t.Helper()
	resp, err := h.client.Get(h.server.URL + "/settings/api-key")
	if err != nil {
		t.Fatalf("GET /settings/api-key: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	m := csrfRe.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no CSRF token in form:\n%s", body)
	}
	return string(m[1])
}

func (h *apiKeyWebHarness) postForm(t *testing.T, path string, form url.Values, htmx bool) (*http.Response, string) {
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

func TestNewAPIKeyWebHandlers_NilDependenciesPanic(t *testing.T) {
	keys := &fakeAPIKeyService{}
	sm := scs.New()
	tests := []struct {
		name string
		fn   func()
	}{
		{"nil api key service", func() { adapter.NewAPIKeyWebHandlers(nil, sm, testUsersLayout, testLogger()) }},
		{"nil session manager", func() { adapter.NewAPIKeyWebHandlers(keys, nil, testUsersLayout, testLogger()) }},
		{"nil layout", func() { adapter.NewAPIKeyWebHandlers(keys, sm, nil, testLogger()) }},
		{"nil logger", func() { adapter.NewAPIKeyWebHandlers(keys, sm, testUsersLayout, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewAPIKeyWebHandlers(%s) did not panic", tt.name)
				}
			}()
			tt.fn()
		})
	}
}

func TestAPIKeyWebHandlers_View_NoCurrentKey_ShowsCreateForm(t *testing.T) {
	h := newAPIKeyWebHarness(t, &fakeAPIKeyService{})

	resp, err := h.client.Get(h.server.URL + "/settings/api-key")
	if err != nil {
		t.Fatalf("GET /settings/api-key: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Create key") {
		t.Error("no current key must render the create form")
	}
	if strings.Contains(string(body), "Rotate") {
		t.Error("no current key must not render the rotate control")
	}
}

func TestAPIKeyWebHandlers_View_WithCurrentKey_ShowsRotateAndRevoke(t *testing.T) {
	current := &domain.APIKey{ID: domain.NewAPIKeyID(), KeyPrefix: "ns_deadbeef", Label: "Nestova integration", CreatedAt: time.Now()}
	h := newAPIKeyWebHarness(t, &fakeAPIKeyService{current: current, hasCurrent: true, list: []*domain.APIKey{current}})

	resp, err := h.client.Get(h.server.URL + "/settings/api-key")
	if err != nil {
		t.Fatalf("GET /settings/api-key: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if !strings.Contains(string(body), "Rotate") {
		t.Errorf("an existing current key must render the rotate control:\n%s", body)
	}
	if !strings.Contains(string(body), "Revoke") {
		t.Errorf("an existing current key must render the revoke control:\n%s", body)
	}
	if strings.Contains(string(body), "Create key") {
		t.Error("an existing current key must not render the create form")
	}
}

func TestAPIKeyWebHandlers_View_HTMXRequest_NoLayout(t *testing.T) {
	h := newAPIKeyWebHarness(t, &fakeAPIKeyService{})

	req, err := http.NewRequest(http.MethodGet, h.server.URL+"/settings/api-key", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("HX-Request", "true")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET /settings/api-key (HTMX): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if strings.Contains(string(body), "<layout>") {
		t.Error("an HTMX request must get the bare fragment, not the layout-wrapped page")
	}
}

func TestAPIKeyWebHandlers_Create_MissingCSRF_Forbidden(t *testing.T) {
	keys := &fakeAPIKeyService{}
	h := newAPIKeyWebHarness(t, keys)

	resp, _ := h.postForm(t, "/settings/api-key", url.Values{"label": {"Nestova integration"}}, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if keys.createCalls != 0 {
		t.Error("APIKeyService.Create called despite a missing CSRF token")
	}
}

func TestAPIKeyWebHandlers_Create_Success_RevealsSecretOnce(t *testing.T) {
	keys := &fakeAPIKeyService{}
	h := newAPIKeyWebHarness(t, keys)
	csrf := h.getCSRF(t)

	resp, body := h.postForm(t, "/settings/api-key", url.Values{"csrf_token": {csrf}, "label": {"Nestova integration"}}, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, domain.APIKeyPrefix+strings.Repeat("a", 64)) {
		t.Errorf("create response missing the one-time secret reveal:\n%s", body)
	}
	if keys.createCalls != 1 {
		t.Errorf("APIKeyService.Create called %d times, want 1", keys.createCalls)
	}

	// A subsequent navigation must never show the secret again.
	getResp, err := h.client.Get(h.server.URL + "/settings/api-key")
	if err != nil {
		t.Fatalf("GET /settings/api-key: %v", err)
	}
	defer func() { _ = getResp.Body.Close() }()
	getBody, _ := io.ReadAll(getResp.Body)
	if strings.Contains(string(getBody), domain.APIKeyPrefix+strings.Repeat("a", 64)) {
		t.Error("the raw secret must not appear in any response after the one that created it")
	}
}

func TestAPIKeyWebHandlers_Create_BlankLabel_MapsTo422(t *testing.T) {
	keys := &fakeAPIKeyService{createErr: domain.ErrInvalidAPIKey}
	h := newAPIKeyWebHarness(t, keys)
	csrf := h.getCSRF(t)

	resp, body := h.postForm(t, "/settings/api-key", url.Values{"csrf_token": {csrf}, "label": {""}}, false)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "enter a label") {
		t.Errorf("body missing the invalid-label message:\n%s", body)
	}
}

func TestAPIKeyWebHandlers_Create_AlreadyExists_MapsTo409(t *testing.T) {
	keys := &fakeAPIKeyService{createErr: domain.ErrAPIKeyExists}
	h := newAPIKeyWebHarness(t, keys)
	csrf := h.getCSRF(t)

	resp, body := h.postForm(t, "/settings/api-key", url.Values{"csrf_token": {csrf}, "label": {"Nestova integration"}}, false)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "already exists") {
		t.Errorf("body missing the already-exists message:\n%s", body)
	}
}

func TestAPIKeyWebHandlers_Create_UnrecognizedError_MapsTo500(t *testing.T) {
	keys := &fakeAPIKeyService{createErr: errors.New("unexpected database explosion")}
	h := newAPIKeyWebHarness(t, keys)
	csrf := h.getCSRF(t)

	resp, body := h.postForm(t, "/settings/api-key", url.Values{"csrf_token": {csrf}, "label": {"Nestova integration"}}, false)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500:\n%s", resp.StatusCode, body)
	}
	if strings.Contains(body, "database explosion") {
		t.Error("the unrecognized error's detail must not leak into the response body")
	}
}

func TestAPIKeyWebHandlers_Rotate_Success_RevealsNewSecret(t *testing.T) {
	current := &domain.APIKey{ID: domain.NewAPIKeyID(), KeyPrefix: "ns_deadbeef", Label: "Nestova integration", CreatedAt: time.Now()}
	keys := &fakeAPIKeyService{current: current, hasCurrent: true, list: []*domain.APIKey{current}}
	h := newAPIKeyWebHarness(t, keys)
	csrf := h.getCSRF(t)

	resp, body := h.postForm(t, "/settings/api-key/rotate", url.Values{"csrf_token": {csrf}, "label": {"Nestova integration"}, "overlap": {"24h"}}, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, domain.APIKeyPrefix+strings.Repeat("b", 64)) {
		t.Errorf("rotate response missing the one-time secret reveal:\n%s", body)
	}
	if keys.rotateCalls != 1 {
		t.Errorf("APIKeyService.Rotate called %d times, want 1", keys.rotateCalls)
	}
}

func TestAPIKeyWebHandlers_Rotate_InvalidOverlap_MapsTo422(t *testing.T) {
	keys := &fakeAPIKeyService{}
	h := newAPIKeyWebHarness(t, keys)
	csrf := h.getCSRF(t)

	resp, body := h.postForm(t, "/settings/api-key/rotate", url.Values{"csrf_token": {csrf}, "label": {"Nestova integration"}, "overlap": {"forever"}}, false)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "valid overlap") {
		t.Errorf("body missing the invalid-overlap message:\n%s", body)
	}
	if keys.rotateCalls != 0 {
		t.Error("APIKeyService.Rotate called despite an invalid overlap window")
	}
}

func TestAPIKeyWebHandlers_Rotate_NotFound_MapsTo404(t *testing.T) {
	keys := &fakeAPIKeyService{rotateErr: domain.ErrAPIKeyNotFound}
	h := newAPIKeyWebHarness(t, keys)
	csrf := h.getCSRF(t)

	resp, body := h.postForm(t, "/settings/api-key/rotate", url.Values{"csrf_token": {csrf}, "label": {"Nestova integration"}, "overlap": {"none"}}, false)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404:\n%s", resp.StatusCode, body)
	}
}

func TestAPIKeyWebHandlers_Revoke_Success(t *testing.T) {
	current := &domain.APIKey{ID: domain.NewAPIKeyID(), KeyPrefix: "ns_deadbeef", Label: "Nestova integration", CreatedAt: time.Now()}
	keys := &fakeAPIKeyService{current: current, hasCurrent: true, list: []*domain.APIKey{current}}
	h := newAPIKeyWebHarness(t, keys)
	csrf := h.getCSRF(t)

	resp, body := h.postForm(t, "/settings/api-key/revoke", url.Values{"csrf_token": {csrf}, "id": {current.ID.String()}}, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if keys.revokeCalls != 1 {
		t.Errorf("APIKeyService.Revoke called %d times, want 1", keys.revokeCalls)
	}
}

func TestAPIKeyWebHandlers_Revoke_MalformedID_BadRequest(t *testing.T) {
	keys := &fakeAPIKeyService{}
	h := newAPIKeyWebHarness(t, keys)
	csrf := h.getCSRF(t)

	resp, body := h.postForm(t, "/settings/api-key/revoke", url.Values{"csrf_token": {csrf}, "id": {"not-a-uuid"}}, false)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400:\n%s", resp.StatusCode, body)
	}
	if keys.revokeCalls != 0 {
		t.Error("APIKeyService.Revoke called despite a malformed id")
	}
}

func TestAPIKeyWebHandlers_Revoke_NotFound_MapsTo404(t *testing.T) {
	keys := &fakeAPIKeyService{revokeErr: domain.ErrAPIKeyNotFound}
	h := newAPIKeyWebHarness(t, keys)
	csrf := h.getCSRF(t)

	resp, body := h.postForm(t, "/settings/api-key/revoke", url.Values{"csrf_token": {csrf}, "id": {domain.NewAPIKeyID().String()}}, false)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404:\n%s", resp.StatusCode, body)
	}
}
