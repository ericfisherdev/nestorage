package adapter_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/api"
)

// newAPIPrincipalServer starts an httptest.Server serving routes (registered
// by registerRoutes on the api.Router it builds) behind the real
// identityadapter.Resolve middleware, resolved to viewer on every request —
// the session-less analog of web_common_test.go's own newPrincipalServer:
// every /api/v1 route carries no session and no CSRF, so unlike its web
// counterpart this fixture never wraps the handler in sm.LoadAndSave.
func newAPIPrincipalServer(t *testing.T, viewer identity.Principal, registerRoutes func(api.Registrar)) *httptest.Server {
	t.Helper()
	apiRouter := api.NewRouter(testLogger())
	registerRoutes(apiRouter)

	chain := identityadapter.NewChain(fixedPrincipalResolver{principal: viewer}, absentCredentialResolver{}, absentCredentialResolver{})
	denier := identityadapter.NewDenier(testLogger())
	resolve := identityadapter.Resolve(chain, denier, testLogger())

	server := httptest.NewServer(resolve(apiRouter.Handler()))
	t.Cleanup(server.Close)
	return server
}

// apiRequest issues method against server.URL+path with an optional body.
// Deliberately carries NO Authorization header: newAPIPrincipalServer places
// fixedPrincipalResolver in the Chain's session slot, mirroring
// newPrincipalServer's own fixture (web_common_test.go) exactly —
// identityadapter.Chain.Resolve only consults the session slot when the
// header is absent (its own doc), dispatching a PRESENT bearer header by
// prefix to the device-token/api-key resolvers instead, which this fixture
// leaves as absentCredentialResolver. The caller is responsible for closing
// the returned response's body.
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

func apiPost(t *testing.T, server *httptest.Server, path, body string) *http.Response {
	t.Helper()
	return apiRequest(t, server, http.MethodPost, path, body)
}

func apiPut(t *testing.T, server *httptest.Server, path, body string) *http.Response {
	t.Helper()
	return apiRequest(t, server, http.MethodPut, path, body)
}

func apiDelete(t *testing.T, server *httptest.Server, path string) *http.Response {
	t.Helper()
	return apiRequest(t, server, http.MethodDelete, path, "")
}

// assertErrorCode decodes resp's body as the shared envelope (platform/api)
// and asserts its error code.
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

// assertFieldDetail decodes resp's body as the shared envelope and asserts
// its details slice names wantField.
func assertFieldDetail(t *testing.T, resp *http.Response, wantField string) {
	t.Helper()
	var body struct {
		Error struct {
			Details []struct {
				Field string `json:"field"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	for _, d := range body.Error.Details {
		if d.Field == wantField {
			return
		}
	}
	t.Errorf("error details = %+v, want a detail naming field %q", body.Error.Details, wantField)
}
