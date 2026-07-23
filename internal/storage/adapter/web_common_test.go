package adapter_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// fixedPrincipalResolver is an identityadapter.Resolver that always reports
// principal for every request, standing in for NSTR-20's real session
// resolver — CurrentPrincipal's context key is unexported to
// identity/adapter, so driving a request through the real
// identityadapter.Resolve middleware (fed by this fake in the "session"
// slot of a Chain) is the only way BinsWebHandlers/LocationsWebHandlers'
// tests, in a different package, can inject one.
type fixedPrincipalResolver struct {
	principal identity.Principal
}

func (f fixedPrincipalResolver) Resolve(_ context.Context, _ *http.Request) (identity.Principal, bool, error) {
	return f.principal, true, nil
}

// absentCredentialResolver always reports its own credential absent — used
// for the deviceToken/apiKey slots of a Chain when a test only cares about
// the session slot.
type absentCredentialResolver struct{}

func (absentCredentialResolver) Resolve(_ context.Context, _ *http.Request) (identity.Principal, bool, error) {
	return identity.Principal{}, false, nil
}

// newPrincipalServer starts an httptest.Server serving routes (registered
// by registerRoutes on the mux it builds) behind sm.LoadAndSave (so
// session.CSRFToken/VerifyCSRF work) and the real identityadapter.Resolve
// middleware, resolved to viewer on every request — the fixture
// BinsWebHandlers/LocationsWebHandlers' own tests share to exercise
// viewer-scoped behavior without a real session cookie or database.
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

// newCSRFClient returns an http.Client with a cookie jar (so the session
// cookie survives across requests) that does not auto-follow redirects,
// letting a test assert a 303's Location header directly — mirroring
// identity/adapter's own usersWebHarness client.
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
// full navigation was wrapped by it and an HTMX request was not, without
// depending on cmd/server's real shell layout — mirrors identity/adapter's
// own testUsersLayout, shared here by BinsWebHandlers and
// LocationsWebHandlers' tests alike.
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
