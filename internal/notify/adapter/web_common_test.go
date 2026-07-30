package adapter_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// csrfRe extracts a rendered form's CSRF token — mirrors storage/adapter's
// own identical, unexported-to-its-package copy (neither can be reused
// directly across bounded-context packages).
var csrfRe = regexp.MustCompile(`name="csrf_token"\s+value="([^"]*)"`)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// fixedPrincipalResolver is an identityadapter.Resolver that always reports
// principal — mirrors storage/adapter's own identical test fixture.
type fixedPrincipalResolver struct {
	principal identity.Principal
}

func (f fixedPrincipalResolver) Resolve(_ context.Context, _ *http.Request) (identity.Principal, bool, error) {
	return f.principal, true, nil
}

// absentCredentialResolver always reports its own credential absent.
type absentCredentialResolver struct{}

func (absentCredentialResolver) Resolve(_ context.Context, _ *http.Request) (identity.Principal, bool, error) {
	return identity.Principal{}, false, nil
}

// newPrincipalServer starts an httptest.Server serving routes behind
// sm.LoadAndSave (so session.CSRFToken/VerifyCSRF work) and the real
// identityadapter.Resolve middleware, resolved to viewer on every request —
// mirrors storage/adapter's own identical fixture (web_common_test.go).
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

// newCSRFClient returns an http.Client with a cookie jar that does not
// auto-follow redirects — mirrors storage/adapter's own identical fixture.
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

// testLayout wraps content in an identifiable marker so a test can assert a
// full navigation was wrapped by it and an HTMX request was not — mirrors
// storage/adapter's own identical fixture.
func testLayout(_ *http.Request, content templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if _, err := io.WriteString(w, "<layout>"); err != nil {
			return err
		}
		if err := content.Render(ctx, w); err != nil {
			return err
		}
		_, err := io.WriteString(w, "</layout>")
		return err
	})
}

func testViewer() identity.Principal {
	return identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Alex")
}
