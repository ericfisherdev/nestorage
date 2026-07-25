package adapter_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/labels/adapter"
	labelsapp "github.com/ericfisherdev/nestorage/internal/labels/app"
	"github.com/ericfisherdev/nestorage/internal/labels/domain"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func testViewer() identity.Principal {
	return identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")
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

// newLabelsWebHarness starts an httptest.Server serving LabelsWebHandlers'
// routes behind the real identityadapter.Resolve middleware, resolved to
// viewer on every request — no *scs.SessionManager wrapping needed, unlike
// storage/adapter's own newPrincipalServer, since Resolve itself has no
// session dependency and this handler group performs no CSRF check (see
// LabelsWebHandlers.Routes' own doc for why: every route here is a read).
func newLabelsWebHarness(t *testing.T, viewer identity.Principal, batches *fakeBatchRenderer) *httptest.Server {
	t.Helper()
	handlers := adapter.NewLabelsWebHandlers(batches, testLogger(), "")
	mux := http.NewServeMux()
	handlers.Routes(mux)

	chain := identityadapter.NewChain(fixedPrincipalResolver{principal: viewer}, absentCredentialResolver{}, absentCredentialResolver{})
	denier := identityadapter.NewDenier(testLogger())
	resolve := identityadapter.Resolve(chain, denier, testLogger())

	server := httptest.NewServer(resolve(mux))
	t.Cleanup(server.Close)
	return server
}

func TestNewLabelsWebHandlers_NilDependenciesPanic(t *testing.T) {
	batches := &fakeBatchRenderer{}
	tests := []struct {
		name string
		fn   func()
	}{
		{"nil batchRenderer", func() { adapter.NewLabelsWebHandlers(nil, testLogger(), "") }},
		{"nil logger", func() { adapter.NewLabelsWebHandlers(batches, nil, "") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewLabelsWebHandlers(%s) did not panic", tt.name)
				}
			}()
			tt.fn()
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
	server := newLabelsWebHarness(t, viewer, batches)

	resp, err := http.Get(server.URL + "/locations/" + locationID.String() + "/labels.pdf?size=avery-5160&offset=3")
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
	server := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf?size=avery-5160")
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
	server := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(server.URL + "/locations/not-a-uuid/labels.pdf?size=avery-5160")
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
	server := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf")
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
	server := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf?size=avery-5160&offset=not-a-number")
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
	server := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf?size=avery-5160")
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
	server := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf?size=not-real")
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
	server := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf?size=avery-5160&offset=999")
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
	server := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf?size=avery-5160")
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
	server := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf?size=avery-5160")
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
	server := newLabelsWebHarness(t, testViewer(), batches)

	resp, err := http.Get(server.URL + "/locations/" + storagedomain.NewLocationID().String() + "/labels.pdf?size=avery-5160")
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
