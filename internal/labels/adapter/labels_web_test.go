package adapter_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/labels/adapter"
	labelsapp "github.com/ericfisherdev/nestorage/internal/labels/app"
	"github.com/ericfisherdev/nestorage/internal/labels/domain"
	storageapp "github.com/ericfisherdev/nestorage/internal/storage/app"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// csrfRe extracts a rendered form's CSRF token, mirroring storage/adapter's
// own csrfRe (a different, unexported-to-its-package copy — this test
// package cannot reuse it directly).
var csrfRe = regexp.MustCompile(`name="csrf_token"\s+value="([^"]*)"`)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func testViewer() identity.Principal {
	return identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")
}

// testLayout wraps content in an identifiable marker so a test can assert a
// full navigation was wrapped by it and an HTMX request was not — mirrors
// storage/adapter's own testLayout (a different, unexported-to-its-package
// copy).
func testLayout(_ *http.Request, content templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if _, err := io.WriteString(w, "<layout>"); err != nil {
			return err
		}
		return content.Render(ctx, w)
	})
}

// fixedPrincipalResolver is an identityadapter.Resolver that always reports
// principal for every request — the same fake storage/adapter's own tests
// use to drive a request through the real identityadapter.Resolve
// middleware (CurrentPrincipal's context key is unexported to
// identity/adapter, so this is the only way a different package's test can
// inject one). Duplicated here rather than shared: it is a two-line type,
// and it is test-only code in both places.
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

// newPrincipalServer starts an httptest.Server serving routes behind
// sm.LoadAndSave (so session.CSRFToken/VerifyCSRF work) and the real
// identityadapter.Resolve middleware, resolved to viewer on every request —
// mirrors storage/adapter's own newPrincipalServer (a different,
// unexported-to-its-package copy).
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
// cookie survives across requests) that does not auto-follow redirects —
// mirrors storage/adapter's own newCSRFClient.
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

// fakeBatchRenderer is a configurable batchRenderer fake for
// LabelsWebHandlers' hermetic unit tests, capturing every argument it was
// last called with.
type fakeBatchRenderer struct {
	doc *labelsapp.BatchDocument
	err error

	calls         int
	gotViewer     identity.Principal
	gotLocationID storagedomain.LocationID
	gotSizeID     domain.LabelSizeID
	gotOffset     int
	gotBaseURL    string
}

func (f *fakeBatchRenderer) RenderLocationBatch(_ context.Context, viewer identity.Principal, locationID storagedomain.LocationID, sizeID domain.LabelSizeID, startOffset int, baseURL string) (*labelsapp.BatchDocument, error) {
	f.calls++
	f.gotViewer = viewer
	f.gotLocationID = locationID
	f.gotSizeID = sizeID
	f.gotOffset = startOffset
	f.gotBaseURL = baseURL
	if f.err != nil {
		return nil, f.err
	}
	return f.doc, nil
}

// fakeBinGetter is a configurable binGetter fake.
type fakeBinGetter struct {
	views map[storagedomain.BinID]storageapp.BinView
	err   error
}

func newFakeBinGetter() *fakeBinGetter {
	return &fakeBinGetter{views: map[storagedomain.BinID]storageapp.BinView{}}
}

func (f *fakeBinGetter) addBin(v storageapp.BinView) { f.views[v.Bin.ID] = v }

func (f *fakeBinGetter) GetByID(_ context.Context, _ identity.Principal, id storagedomain.BinID) (*storageapp.BinView, error) {
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.views[id]
	if !ok {
		return nil, storagedomain.ErrBinNotFound
	}
	return &v, nil
}

// fakeLocationGetter is a configurable locationGetter fake.
type fakeLocationGetter struct {
	locations map[storagedomain.LocationID]storagedomain.Location
	err       error
}

func newFakeLocationGetter() *fakeLocationGetter {
	return &fakeLocationGetter{locations: map[storagedomain.LocationID]storagedomain.Location{}}
}

func (f *fakeLocationGetter) addLocation(l storagedomain.Location) { f.locations[l.ID] = l }

func (f *fakeLocationGetter) Get(_ context.Context, _ identity.Principal, id storagedomain.LocationID) (*storagedomain.Location, error) {
	if f.err != nil {
		return nil, f.err
	}
	l, ok := f.locations[id]
	if !ok {
		return nil, storagedomain.ErrLocationNotFound
	}
	return &l, nil
}

// fakeLocationBinLister is a configurable locationBinLister fake.
type fakeLocationBinLister struct {
	bins map[storagedomain.LocationID][]storageapp.BinView
	err  error
}

func newFakeLocationBinLister() *fakeLocationBinLister {
	return &fakeLocationBinLister{bins: map[storagedomain.LocationID][]storageapp.BinView{}}
}

func (f *fakeLocationBinLister) setBins(locationID storagedomain.LocationID, bins []storageapp.BinView) {
	f.bins[locationID] = bins
}

func (f *fakeLocationBinLister) ListVisibleByLocation(_ context.Context, _ identity.Principal, locationID storagedomain.LocationID) ([]storageapp.BinView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.bins[locationID], nil
}

// fakePreferenceRepository is a configurable domain.SizePreferenceRepository
// fake, capturing every Upsert call.
type fakePreferenceRepository struct {
	stored    map[identity.UserID]domain.LabelSizeID
	getErr    error
	upsertErr error

	upsertCalls    int
	lastUpsertUser identity.UserID
	lastUpsertSize domain.LabelSizeID
}

func newFakePreferenceRepository() *fakePreferenceRepository {
	return &fakePreferenceRepository{stored: map[identity.UserID]domain.LabelSizeID{}}
}

func (f *fakePreferenceRepository) Get(_ context.Context, userID identity.UserID) (domain.LabelSizeID, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	sizeID, ok := f.stored[userID]
	if !ok {
		return "", domain.ErrPreferenceNotFound
	}
	return sizeID, nil
}

func (f *fakePreferenceRepository) Upsert(_ context.Context, userID identity.UserID, sizeID domain.LabelSizeID) error {
	f.upsertCalls++
	f.lastUpsertUser = userID
	f.lastUpsertSize = sizeID
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.stored[userID] = sizeID
	return nil
}

// labelsWebHarness bundles a running LabelsWebHandlers server with every
// fake dependency, so a test can both drive requests and assert on what the
// handler read or wrote.
type labelsWebHarness struct {
	server       *httptest.Server
	client       *http.Client
	batches      *fakeBatchRenderer
	bins         *fakeBinGetter
	locations    *fakeLocationGetter
	locationBins *fakeLocationBinLister
	preferences  *fakePreferenceRepository
}

// newLabelsWebHarness builds a harness with fresh, empty fakes for every
// dependency but batches — the shape most of Download's own pre-existing
// tests need, since they exercise only the batch-download route.
func newLabelsWebHarness(t *testing.T, viewer identity.Principal, batches *fakeBatchRenderer) *labelsWebHarness {
	t.Helper()
	return newLabelsWebHarnessFull(t, viewer, batches, newFakeBinGetter(), newFakeLocationGetter(), newFakeLocationBinLister(), newFakePreferenceRepository())
}

func newLabelsWebHarnessFull(t *testing.T, viewer identity.Principal, batches *fakeBatchRenderer, bins *fakeBinGetter, locations *fakeLocationGetter, locationBins *fakeLocationBinLister, preferences *fakePreferenceRepository) *labelsWebHarness {
	t.Helper()
	sizes, err := domain.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	sm := scs.New()
	handlers := adapter.NewLabelsWebHandlers(adapter.LabelsWebHandlersDeps{
		Bins: bins, Locations: locations, LocationBins: locationBins,
		Sizes: sizes, Preferences: preferences, Batches: batches,
		SM: sm, Layout: testLayout, Logger: testLogger(),
	})
	server := newPrincipalServer(t, sm, viewer, handlers.Routes)
	return &labelsWebHarness{
		server: server, client: newCSRFClient(t), batches: batches,
		bins: bins, locations: locations, locationBins: locationBins, preferences: preferences,
	}
}

func (h *labelsWebHarness) getCSRF(t *testing.T, path string) string {
	t.Helper()
	resp, err := h.client.Get(h.server.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	m := csrfRe.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no CSRF token in form:\n%s", body)
	}
	return string(m[1])
}

func (h *labelsWebHarness) postForm(t *testing.T, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.server.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(body)
}

func TestNewLabelsWebHandlers_NilDependenciesPanic(t *testing.T) {
	sizes, err := domain.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	base := adapter.LabelsWebHandlersDeps{
		Bins: newFakeBinGetter(), Locations: newFakeLocationGetter(), LocationBins: newFakeLocationBinLister(),
		Sizes: sizes, Preferences: newFakePreferenceRepository(), Batches: &fakeBatchRenderer{},
		SM: scs.New(), Layout: testLayout, Logger: testLogger(),
	}
	tests := []struct {
		name   string
		mutate func(*adapter.LabelsWebHandlersDeps)
	}{
		{"nil bin getter", func(d *adapter.LabelsWebHandlersDeps) { d.Bins = nil }},
		{"nil location getter", func(d *adapter.LabelsWebHandlersDeps) { d.Locations = nil }},
		{"nil location bin lister", func(d *adapter.LabelsWebHandlersDeps) { d.LocationBins = nil }},
		{"nil size registry", func(d *adapter.LabelsWebHandlersDeps) { d.Sizes = nil }},
		{"nil preference repository", func(d *adapter.LabelsWebHandlersDeps) { d.Preferences = nil }},
		{"nil batch renderer", func(d *adapter.LabelsWebHandlersDeps) { d.Batches = nil }},
		{"nil session manager", func(d *adapter.LabelsWebHandlersDeps) { d.SM = nil }},
		{"nil layout", func(d *adapter.LabelsWebHandlersDeps) { d.Layout = nil }},
		{"nil logger", func(d *adapter.LabelsWebHandlersDeps) { d.Logger = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewLabelsWebHandlers(%s) did not panic", tt.name)
				}
			}()
			deps := base
			tt.mutate(&deps)
			adapter.NewLabelsWebHandlers(deps)
		})
	}
}

func TestLabelsWebHandlers_Download_Success(t *testing.T) {
	locationID := storagedomain.NewLocationID()
	batches := &fakeBatchRenderer{doc: &labelsapp.BatchDocument{
		Document: domain.Document{ContentType: "application/pdf", Data: []byte("pdf-bytes")},
		Filename: "labels-garage-avery-5160.pdf",
	}}
	viewer := testViewer()
	h := newLabelsWebHarness(t, viewer, batches)

	resp, err := http.Get(h.server.URL + "/locations/" + locationID.String() + "/labels.pdf?size=avery-5160&offset=3")
	if err != nil {
		t.Fatalf("GET labels.pdf: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/pdf" {
		t.Errorf("Content-Type = %q, want application/pdf", got)
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.Contains(got, "labels-garage-avery-5160.pdf") {
		t.Errorf("Content-Disposition = %q, want it to name the batch's filename", got)
	}
	if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length = %q, want it to match the response body's own length (%d)", got, len(body))
	}
	if string(body) != "pdf-bytes" {
		t.Errorf("body = %q, want the batch's own Data", body)
	}

	if batches.gotLocationID != locationID {
		t.Errorf("RenderLocationBatch locationID = %v, want %v", batches.gotLocationID, locationID)
	}
	if batches.gotSizeID != "avery-5160" {
		t.Errorf("RenderLocationBatch sizeID = %q, want %q", batches.gotSizeID, "avery-5160")
	}
	if batches.gotOffset != 3 {
		t.Errorf("RenderLocationBatch startOffset = %d, want 3", batches.gotOffset)
	}
	if batches.gotViewer != viewer {
		t.Errorf("RenderLocationBatch viewer = %+v, want %+v", batches.gotViewer, viewer)
	}
}

func TestLabelsWebHandlers_Download_DefaultOffsetIsZero(t *testing.T) {
	batches := &fakeBatchRenderer{doc: &labelsapp.BatchDocument{Document: domain.Document{ContentType: "application/pdf", Data: []byte("x")}, Filename: "labels.pdf"}}
	h := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(h.server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf?size=avery-5160")
	if err != nil {
		t.Fatalf("GET labels.pdf: %v", err)
	}
	_ = resp.Body.Close()

	if batches.gotOffset != 0 {
		t.Errorf("RenderLocationBatch startOffset = %d, want 0 (query omitted)", batches.gotOffset)
	}
}

func TestLabelsWebHandlers_Download_MalformedLocationID(t *testing.T) {
	batches := &fakeBatchRenderer{}
	h := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(h.server.URL + "/locations/not-a-uuid/labels.pdf?size=avery-5160")
	if err != nil {
		t.Fatalf("GET labels.pdf: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if batches.calls != 0 {
		t.Error("RenderLocationBatch must not be called for a malformed location id")
	}
}

func TestLabelsWebHandlers_Download_MissingSize(t *testing.T) {
	batches := &fakeBatchRenderer{}
	h := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(h.server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf")
	if err != nil {
		t.Fatalf("GET labels.pdf: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if batches.calls != 0 {
		t.Error("RenderLocationBatch must not be called when size is missing")
	}
}

func TestLabelsWebHandlers_Download_MalformedOffset(t *testing.T) {
	batches := &fakeBatchRenderer{}
	h := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(h.server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf?size=avery-5160&offset=not-a-number")
	if err != nil {
		t.Fatalf("GET labels.pdf: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if batches.calls != 0 {
		t.Error("RenderLocationBatch must not be called for a malformed offset")
	}
}

func TestLabelsWebHandlers_Download_LocationNotFound(t *testing.T) {
	batches := &fakeBatchRenderer{err: storagedomain.ErrLocationNotFound}
	h := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(h.server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf?size=avery-5160")
	if err != nil {
		t.Fatalf("GET labels.pdf: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestLabelsWebHandlers_Download_UnknownSize(t *testing.T) {
	batches := &fakeBatchRenderer{err: domain.ErrUnknownLabelSize}
	h := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(h.server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf?size=not-real")
	if err != nil {
		t.Fatalf("GET labels.pdf: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestLabelsWebHandlers_Download_InvalidStartOffset(t *testing.T) {
	batches := &fakeBatchRenderer{err: domain.ErrInvalidStartOffset}
	h := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(h.server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf?size=avery-5160&offset=999")
	if err != nil {
		t.Fatalf("GET labels.pdf: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestLabelsWebHandlers_Download_EmptyBatch(t *testing.T) {
	batches := &fakeBatchRenderer{err: domain.ErrEmptyBatch}
	h := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(h.server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf?size=avery-5160")
	if err != nil {
		t.Fatalf("GET labels.pdf: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "no bins you can print") {
		t.Errorf("body = %q, want the empty-batch message", body)
	}
}

func TestLabelsWebHandlers_Download_TooLarge(t *testing.T) {
	batches := &fakeBatchRenderer{err: &domain.BatchTooLargeError{Count: 412, Max: domain.MaxBatchLabels}}
	h := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(h.server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf?size=avery-5160")
	if err != nil {
		t.Fatalf("GET labels.pdf: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "412") || !strings.Contains(string(body), "300") {
		t.Errorf("body = %q, want it to name both the count (412) and the cap (300)", body)
	}
}

func TestLabelsWebHandlers_Download_UnexpectedErrorIsGeneric500(t *testing.T) {
	batches := &fakeBatchRenderer{err: errors.New("boom: connection reset by peer")}
	h := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(h.server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf?size=avery-5160")
	if err != nil {
		t.Fatalf("GET labels.pdf: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500:\n%s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "boom") {
		t.Errorf("body = %q, must not leak the underlying error", body)
	}
}

// seedLocationWithBins registers locationID/name in locations and count
// distinct visible bins under it in locationBins, returning the created
// bins in the same code order ListVisibleByLocation's own contract
// promises.
func seedLocationWithBins(locations *fakeLocationGetter, locationBins *fakeLocationBinLister, locationID storagedomain.LocationID, name string, count int) []storageapp.BinView {
	locations.addLocation(storagedomain.Location{ID: locationID, Name: name})
	bins := make([]storageapp.BinView, count)
	for i := range bins {
		bins[i] = storageapp.BinView{Bin: storagedomain.Bin{
			ID: storagedomain.NewBinID(), Code: "BIN-" + strconv.Itoa(i), Name: "Bin " + strconv.Itoa(i), LocationID: locationID,
		}}
	}
	locationBins.setBins(locationID, bins)
	return bins
}

func TestLabelsWebHandlers_PrintScreen_LocationTarget_Success(t *testing.T) {
	locations, locationBins := newFakeLocationGetter(), newFakeLocationBinLister()
	locationID := storagedomain.NewLocationID()
	seedLocationWithBins(locations, locationBins, locationID, "Garage", 3)
	h := newLabelsWebHarnessFull(t, testViewer(), &fakeBatchRenderer{}, newFakeBinGetter(), locations, locationBins, newFakePreferenceRepository())

	resp, err := h.client.Get(h.server.URL + "/labels/print?location=" + locationID.String())
	if err != nil {
		t.Fatalf("GET /labels/print: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Print labels — Garage") {
		t.Errorf("body missing location header:\n%s", body)
	}
	if !strings.Contains(string(body), "3 bins") {
		t.Errorf("body missing bin count:\n%s", body)
	}
}

// TestLabelsWebHandlers_PrintScreen_BinTarget_Success pins a bin-scoped
// request's InitialOffset at 0, same as a location-scoped one, even when
// the tapped bin is not the location's own first bin (seeded[1] here):
// NSTR-50's batch startOffset only blanks leading cells before bins[0], it
// does not choose which label starts printing, so there is no offset value
// that would land the tapped bin at a fixed cell (see printTarget's own
// doc). A prior version set InitialOffset to the tapped bin's own list
// index, which shifted every bin's own position without ever placing the
// tapped one where expected.
func TestLabelsWebHandlers_PrintScreen_BinTarget_Success(t *testing.T) {
	locations, locationBins, bins := newFakeLocationGetter(), newFakeLocationBinLister(), newFakeBinGetter()
	locationID := storagedomain.NewLocationID()
	seeded := seedLocationWithBins(locations, locationBins, locationID, "Garage", 3)
	target := seeded[1]
	target.Bin.Name = "Winter tires"
	bins.addBin(target)
	h := newLabelsWebHarnessFull(t, testViewer(), &fakeBatchRenderer{}, bins, locations, locationBins, newFakePreferenceRepository())

	resp, err := h.client.Get(h.server.URL + "/labels/print?bin=" + target.Bin.ID.String())
	if err != nil {
		t.Fatalf("GET /labels/print: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Print label — Winter tires") {
		t.Errorf("body missing bin header:\n%s", body)
	}
	if !strings.Contains(string(body), `name="offset" value="0"`) {
		t.Errorf("bin-scoped print screen must start at offset 0, not the tapped bin's own list index:\n%s", body)
	}
}

func TestLabelsWebHandlers_PrintScreen_NoTarget_BadRequest(t *testing.T) {
	h := newLabelsWebHarness(t, testViewer(), &fakeBatchRenderer{})

	resp, err := h.client.Get(h.server.URL + "/labels/print")
	if err != nil {
		t.Fatalf("GET /labels/print: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestLabelsWebHandlers_PrintScreen_BothTargets_BadRequest(t *testing.T) {
	h := newLabelsWebHarness(t, testViewer(), &fakeBatchRenderer{})

	resp, err := h.client.Get(h.server.URL + "/labels/print?bin=" + storagedomain.NewBinID().String() + "&location=" + storagedomain.NewLocationID().String())
	if err != nil {
		t.Fatalf("GET /labels/print: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestLabelsWebHandlers_PrintScreen_BinNotFound(t *testing.T) {
	h := newLabelsWebHarness(t, testViewer(), &fakeBatchRenderer{})

	resp, err := h.client.Get(h.server.URL + "/labels/print?bin=" + storagedomain.NewBinID().String())
	if err != nil {
		t.Fatalf("GET /labels/print: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (unknown/invisible bin)", resp.StatusCode)
	}
}

func TestLabelsWebHandlers_PrintScreen_LocationNotFound(t *testing.T) {
	h := newLabelsWebHarness(t, testViewer(), &fakeBatchRenderer{})

	resp, err := h.client.Get(h.server.URL + "/labels/print?location=" + storagedomain.NewLocationID().String())
	if err != nil {
		t.Fatalf("GET /labels/print: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (unknown/invisible location)", resp.StatusCode)
	}
}

func TestLabelsWebHandlers_PrintScreen_RemembersSize(t *testing.T) {
	locations, locationBins := newFakeLocationGetter(), newFakeLocationBinLister()
	locationID := storagedomain.NewLocationID()
	seedLocationWithBins(locations, locationBins, locationID, "Garage", 1)
	viewer := testViewer()
	preferences := newFakePreferenceRepository()
	preferences.stored[viewer.UserID] = domain.LabelSizeAvery5163
	h := newLabelsWebHarnessFull(t, viewer, &fakeBatchRenderer{}, newFakeBinGetter(), locations, locationBins, preferences)

	resp, err := h.client.Get(h.server.URL + "/labels/print?location=" + locationID.String())
	if err != nil {
		t.Fatalf("GET /labels/print: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if !strings.Contains(string(body), `value="avery-5163"`) || !strings.Contains(string(body), "checked") {
		t.Errorf("body did not preselect the remembered size:\n%s", body)
	}
}

func TestLabelsWebHandlers_PrintPreview_ShowsBins(t *testing.T) {
	locations, locationBins := newFakeLocationGetter(), newFakeLocationBinLister()
	locationID := storagedomain.NewLocationID()
	seedLocationWithBins(locations, locationBins, locationID, "Garage", 2)
	h := newLabelsWebHarnessFull(t, testViewer(), &fakeBatchRenderer{}, newFakeBinGetter(), locations, locationBins, newFakePreferenceRepository())

	resp, err := h.client.Get(h.server.URL + "/labels/print/preview?location=" + locationID.String() + "&size=avery-5160&offset=0")
	if err != nil {
		t.Fatalf("GET preview: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Bin 0") || !strings.Contains(string(body), "Bin 1") {
		t.Errorf("preview did not name the location's own bins:\n%s", body)
	}
}

func TestLabelsWebHandlers_PrintPreview_ClampsOffsetToLastCell(t *testing.T) {
	locations, locationBins := newFakeLocationGetter(), newFakeLocationBinLister()
	locationID := storagedomain.NewLocationID()
	seedLocationWithBins(locations, locationBins, locationID, "Garage", 2)
	h := newLabelsWebHarnessFull(t, testViewer(), &fakeBatchRenderer{}, newFakeBinGetter(), locations, locationBins, newFakePreferenceRepository())

	resp, err := h.client.Get(h.server.URL + "/labels/print/preview?location=" + locationID.String() + "&size=avery-5160&offset=9999")
	if err != nil {
		t.Fatalf("GET preview: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	// avery-5160 has 30 cells; an offset of 9999 must clamp to 29 (the last
	// cell on the previewed first sheet), not be rejected or silently left
	// out of range. Bin 0 lands on that last cell; Bin 1 spills onto a
	// second sheet the preview never renders (only the first sheet is
	// previewed), so this asserts the clamp, not bin names.
	if !strings.Contains(string(body), `name="offset" value="29"`) {
		t.Errorf("preview did not clamp offset to the size's last cell:\n%s", body)
	}
	if !strings.Contains(string(body), "Bin 0") {
		t.Errorf("preview did not show the bin landing on the clamped offset's own cell:\n%s", body)
	}
}

func TestLabelsWebHandlers_PrintPreview_UnknownSize_BadRequest(t *testing.T) {
	locations, locationBins := newFakeLocationGetter(), newFakeLocationBinLister()
	locationID := storagedomain.NewLocationID()
	seedLocationWithBins(locations, locationBins, locationID, "Garage", 1)
	h := newLabelsWebHarnessFull(t, testViewer(), &fakeBatchRenderer{}, newFakeBinGetter(), locations, locationBins, newFakePreferenceRepository())

	resp, err := h.client.Get(h.server.URL + "/labels/print/preview?location=" + locationID.String() + "&size=not-real")
	if err != nil {
		t.Fatalf("GET preview: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestLabelsWebHandlers_Print_Success_PersistsPreferenceAndStreamsPDF(t *testing.T) {
	locations, locationBins := newFakeLocationGetter(), newFakeLocationBinLister()
	locationID := storagedomain.NewLocationID()
	seedLocationWithBins(locations, locationBins, locationID, "Garage", 1)
	batches := &fakeBatchRenderer{doc: &labelsapp.BatchDocument{
		Document: domain.Document{ContentType: "application/pdf", Data: []byte("pdf-bytes")},
		Filename: "labels-garage-avery-5163.pdf",
	}}
	viewer := testViewer()
	preferences := newFakePreferenceRepository()
	h := newLabelsWebHarnessFull(t, viewer, batches, newFakeBinGetter(), locations, locationBins, preferences)

	csrf := h.getCSRF(t, "/labels/print?location="+locationID.String())
	form := url.Values{"csrf_token": {csrf}, "location": {locationID.String()}, "size_id": {"avery-5163"}, "offset": {"0"}}
	resp, body := h.postForm(t, "/labels/print", form)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.Contains(got, "labels-garage-avery-5163.pdf") {
		t.Errorf("Content-Disposition = %q, want it to name the batch's filename", got)
	}
	if body != "pdf-bytes" {
		t.Errorf("body = %q, want the batch's own Data", body)
	}
	if batches.gotLocationID != locationID || batches.gotSizeID != "avery-5163" {
		t.Errorf("RenderLocationBatch got locationID=%v sizeID=%q, want %v/avery-5163", batches.gotLocationID, batches.gotSizeID, locationID)
	}
	if preferences.upsertCalls != 1 || preferences.lastUpsertUser != viewer.UserID || preferences.lastUpsertSize != "avery-5163" {
		t.Errorf("preference upsert = calls=%d user=%v size=%q, want one call for %v/avery-5163", preferences.upsertCalls, preferences.lastUpsertUser, preferences.lastUpsertSize, viewer.UserID)
	}
}

func TestLabelsWebHandlers_Print_MissingCSRF_Forbidden(t *testing.T) {
	locations, locationBins := newFakeLocationGetter(), newFakeLocationBinLister()
	locationID := storagedomain.NewLocationID()
	seedLocationWithBins(locations, locationBins, locationID, "Garage", 1)
	h := newLabelsWebHarnessFull(t, testViewer(), &fakeBatchRenderer{}, newFakeBinGetter(), locations, locationBins, newFakePreferenceRepository())
	h.getCSRF(t, "/labels/print?location="+locationID.String()) // establish the session cookie

	form := url.Values{"csrf_token": {"wrong-token"}, "location": {locationID.String()}, "size_id": {"avery-5160"}, "offset": {"0"}}
	resp, _ := h.postForm(t, "/labels/print", form)

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestLabelsWebHandlers_Print_BatchTooLarge_RerendersInline(t *testing.T) {
	locations, locationBins := newFakeLocationGetter(), newFakeLocationBinLister()
	locationID := storagedomain.NewLocationID()
	seedLocationWithBins(locations, locationBins, locationID, "Garage", 1)
	batches := &fakeBatchRenderer{err: &domain.BatchTooLargeError{Count: 412, Max: domain.MaxBatchLabels}}
	h := newLabelsWebHarnessFull(t, testViewer(), batches, newFakeBinGetter(), locations, locationBins, newFakePreferenceRepository())

	csrf := h.getCSRF(t, "/labels/print?location="+locationID.String())
	form := url.Values{"csrf_token": {csrf}, "location": {locationID.String()}, "size_id": {"avery-5160"}, "offset": {"0"}}
	resp, body := h.postForm(t, "/labels/print", form)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "412") || !strings.Contains(body, "300") {
		t.Errorf("body = %q, want the cap error inline", body)
	}
	if !strings.Contains(body, "Print labels — Garage") {
		t.Errorf("body = %q, want the print screen re-rendered, not a bare error page", body)
	}
}

func TestLabelsWebHandlers_Print_UnknownSize_RerendersInline(t *testing.T) {
	locations, locationBins := newFakeLocationGetter(), newFakeLocationBinLister()
	locationID := storagedomain.NewLocationID()
	seedLocationWithBins(locations, locationBins, locationID, "Garage", 1)
	preferences := newFakePreferenceRepository()
	h := newLabelsWebHarnessFull(t, testViewer(), &fakeBatchRenderer{}, newFakeBinGetter(), locations, locationBins, preferences)

	csrf := h.getCSRF(t, "/labels/print?location="+locationID.String())
	form := url.Values{"csrf_token": {csrf}, "location": {locationID.String()}, "size_id": {"not-real"}, "offset": {"0"}}
	resp, body := h.postForm(t, "/labels/print", form)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if preferences.upsertCalls != 0 {
		t.Error("an unknown size must not be persisted as the remembered preference")
	}
}
