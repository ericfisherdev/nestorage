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
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// fakePrimaryPhotoRefLister is a configurable primaryPhotoRefLister fake for
// BinsWebHandlers/ItemsWebHandlers' hermetic unit tests (NSTR-37), shared
// here (rather than duplicated per _test.go file) since both handler
// groups' own harnesses need it. Every existing harness call site defaults
// to a zero-value instance (no refs, no error) — the "no photos yet" case —
// so the many pre-NSTR-37 tests in this package needed no signature change;
// only the harness helpers' own internals changed to inject one.
type fakePrimaryPhotoRefLister struct {
	refs map[domain.ItemID]domain.PhotoRef
	err  error

	calls [][]domain.ItemID
}

func (f *fakePrimaryPhotoRefLister) ListPrimaryThumbRefs(_ context.Context, _ identity.Principal, itemIDs []domain.ItemID) (map[domain.ItemID]domain.PhotoRef, error) {
	f.calls = append(f.calls, itemIDs)
	if f.err != nil {
		return nil, f.err
	}
	return f.refs, nil
}

// fakeEventLister is a configurable itemEventLister/binActivityLister fake
// for ItemsWebHandlers/BinsWebHandlers' hermetic unit tests (NSTR-42),
// shared here since both handler groups' own harnesses need it — mirrors
// fakePrimaryPhotoRefLister's own shape and rationale (NSTR-37). ItemEvents
// backs ListByItem for a page.Before == nil (first page) call, EventsAfter
// backs one for page.Before != nil (a later page) — a fixed, test-supplied
// answer per call shape rather than a generic keyset simulation, since the
// keyset ordering/boundary behavior itself is NSTR-40's own gated-test
// responsibility (item_event_postgres_gated_test.go), not this package's.
// BinEvents backs ListByBin, truncated to the requested limit exactly as
// the real repository's own fixed-window query would be.
type fakeEventLister struct {
	itemEvents      []domain.ItemEvent
	itemEventsAfter []domain.ItemEvent
	itemErr         error
	itemCalls       []domain.HistoryPage

	binEvents []domain.ItemEvent
	binErr    error
	binCalls  int
}

func (f *fakeEventLister) ListByItem(_ context.Context, _ domain.ItemID, page domain.HistoryPage) ([]domain.ItemEvent, error) {
	f.itemCalls = append(f.itemCalls, page)
	if f.itemErr != nil {
		return nil, f.itemErr
	}
	if page.Before != nil {
		return f.itemEventsAfter, nil
	}
	return f.itemEvents, nil
}

func (f *fakeEventLister) ListByBin(_ context.Context, _ domain.BinID, limit int) ([]domain.ItemEvent, error) {
	f.binCalls++
	if f.binErr != nil {
		return nil, f.binErr
	}
	events := f.binEvents
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
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
