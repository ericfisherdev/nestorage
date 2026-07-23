package adapter_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/alexedwards/scs/v2"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/adapter"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// csrfRe extracts a rendered form's CSRF token, mirroring identity/adapter's
// own csrfRe (a different, unexported-to-its-package copy — this test
// package cannot reuse it directly).
var csrfRe = regexp.MustCompile(`name="csrf_token"\s+value="([^"]*)"`)

// fakeBinService is a configurable binQueryCommandService fake for
// BinsWebHandlers' hermetic unit tests, mirroring identity/adapter's
// fakeAdminService shape.
type fakeBinService struct {
	views  map[domain.BinID]app.BinView
	byCode map[string]domain.BinID

	listErr      error
	getByIDErr   error
	getByCodeErr error
	createErr    error
	editErr      error
	deleteErr    error

	createCalls int
}

func newFakeBinService() *fakeBinService {
	return &fakeBinService{views: map[domain.BinID]app.BinView{}, byCode: map[string]domain.BinID{}}
}

func (f *fakeBinService) addBin(v app.BinView) {
	f.views[v.Bin.ID] = v
	f.byCode[v.Bin.Code] = v.Bin.ID
}

func (f *fakeBinService) ListVisible(_ context.Context, _ identity.Principal) ([]app.BinView, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	views := make([]app.BinView, 0, len(f.views))
	for _, v := range f.views {
		views = append(views, v)
	}
	return views, nil
}

func (f *fakeBinService) GetByID(_ context.Context, _ identity.Principal, id domain.BinID) (*app.BinView, error) {
	if f.getByIDErr != nil {
		return nil, f.getByIDErr
	}
	v, ok := f.views[id]
	if !ok {
		return nil, domain.ErrBinNotFound
	}
	return &v, nil
}

func (f *fakeBinService) GetByCode(_ context.Context, _ identity.Principal, code string) (*app.BinView, error) {
	if f.getByCodeErr != nil {
		return nil, f.getByCodeErr
	}
	id, ok := f.byCode[code]
	if !ok {
		return nil, domain.ErrBinNotFound
	}
	v := f.views[id]
	return &v, nil
}

func (f *fakeBinService) Create(
	_ context.Context, code, name, description string, locationID domain.LocationID,
	ownerID *identity.UserID, visibility domain.Visibility, createdBy identity.UserID,
) (*domain.Bin, error) {
	f.createCalls++
	if f.createErr != nil {
		return nil, f.createErr
	}
	b := domain.Bin{
		ID: domain.NewBinID(), Code: domain.NormalizeBinCode(code), Name: name, Description: description,
		LocationID: locationID, OwnerID: ownerID, Visibility: visibility, CreatedBy: createdBy,
	}
	f.addBin(app.BinView{Bin: b})
	return &b, nil
}

func (f *fakeBinService) Edit(_ context.Context, _ identity.Principal, id domain.BinID, name, description string, ownerID *identity.UserID, visibility domain.Visibility) error {
	if f.editErr != nil {
		return f.editErr
	}
	v, ok := f.views[id]
	if !ok {
		return domain.ErrBinNotFound
	}
	v.Bin.Name, v.Bin.Description, v.Bin.OwnerID, v.Bin.Visibility = name, description, ownerID, visibility
	f.views[id] = v
	return nil
}

func (f *fakeBinService) Delete(_ context.Context, _ identity.Principal, id domain.BinID) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.views[id]; !ok {
		return domain.ErrBinNotFound
	}
	delete(f.views, id)
	return nil
}

// fakeBinMover is a configurable binMover fake driving BinsWebHandlers'
// move route without NSTR-30's real transactional service.
type fakeBinMover struct {
	err   error
	calls int
}

func (f *fakeBinMover) Move(_ context.Context, actor identity.Principal, binID domain.BinID, target domain.LocationID) (app.MoveResult, error) {
	f.calls++
	if f.err != nil {
		return app.MoveResult{}, f.err
	}
	return app.MoveResult{BinID: binID, ToLocationID: target, MovedBy: actor.UserID}, nil
}

// fakeLocationSummaries is a configurable locationLister fake shared by
// BinsWebHandlers' location <select>/name-resolution needs.
type fakeLocationSummaries struct {
	summaries []app.LocationSummary
	err       error
}

func (f *fakeLocationSummaries) List(_ context.Context, _ identity.Principal) ([]app.LocationSummary, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.summaries, nil
}

// fakeMembers is a configurable memberDirectory fake for the bin form's
// owner picker.
type fakeMembers struct {
	users []identity.User
	err   error
}

func (f *fakeMembers) List(_ context.Context) ([]identity.User, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.users, nil
}

// fakeItemLister is a configurable itemLister fake for a bin's read-only
// contents list.
type fakeItemLister struct {
	items []domain.Item
	err   error
}

func (f *fakeItemLister) ListInBin(_ context.Context, _ identity.Principal, _ domain.BinID) ([]domain.Item, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

// binsWebHarness bundles a running BinsWebHandlers server and a client
// carrying its session cookie across requests.
type binsWebHarness struct {
	server *httptest.Server
	client *http.Client
	bins   *fakeBinService
	mover  *fakeBinMover
}

func newBinsWebHarness(t *testing.T, viewer identity.Principal, bins *fakeBinService, mover *fakeBinMover, locations *fakeLocationSummaries, members *fakeMembers, items *fakeItemLister) *binsWebHarness {
	t.Helper()
	sm := scs.New()
	handlers := adapter.NewBinsWebHandlers(bins, mover, locations, members, items, sm, testLayout, testLogger())
	server := newPrincipalServer(t, sm, viewer, handlers.Routes)
	return &binsWebHarness{server: server, client: newCSRFClient(t), bins: bins, mover: mover}
}

func (h *binsWebHarness) getCSRF(t *testing.T, path string) string {
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

func (h *binsWebHarness) postForm(t *testing.T, path string, form url.Values, htmx bool) (*http.Response, string) {
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

func testViewer() identity.Principal {
	return identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")
}

func TestNewBinsWebHandlers_NilDependenciesPanic(t *testing.T) {
	bins, mover, locations, members, items := newFakeBinService(), &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{}
	sm := scs.New()
	tests := []struct {
		name string
		fn   func()
	}{
		{"nil bin service", func() {
			adapter.NewBinsWebHandlers(nil, mover, locations, members, items, sm, testLayout, testLogger())
		}},
		{"nil mover", func() { adapter.NewBinsWebHandlers(bins, nil, locations, members, items, sm, testLayout, testLogger()) }},
		{"nil locations", func() { adapter.NewBinsWebHandlers(bins, mover, nil, members, items, sm, testLayout, testLogger()) }},
		{"nil members", func() { adapter.NewBinsWebHandlers(bins, mover, locations, nil, items, sm, testLayout, testLogger()) }},
		{"nil items", func() { adapter.NewBinsWebHandlers(bins, mover, locations, members, nil, sm, testLayout, testLogger()) }},
		{"nil session manager", func() {
			adapter.NewBinsWebHandlers(bins, mover, locations, members, items, nil, testLayout, testLogger())
		}},
		{"nil layout", func() { adapter.NewBinsWebHandlers(bins, mover, locations, members, items, sm, nil, testLogger()) }},
		{"nil logger", func() { adapter.NewBinsWebHandlers(bins, mover, locations, members, items, sm, testLayout, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewBinsWebHandlers(%s) did not panic", tt.name)
				}
			}()
			tt.fn()
		})
	}
}

func TestBinsWebHandlers_List_FullNavigation_WrapsInLayout(t *testing.T) {
	bins := newFakeBinService()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Winter Clothes"}})
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})

	resp, err := h.client.Get(h.server.URL + "/bins")
	if err != nil {
		t.Fatalf("GET /bins: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "<layout>") {
		t.Error("full navigation response was not wrapped in the layout")
	}
	if !strings.Contains(string(body), "Winter Clothes") {
		t.Error("response missing the seeded bin's name")
	}
}

func TestBinsWebHandlers_List_HTMXRequest_NoLayout(t *testing.T) {
	h := newBinsWebHarness(t, testViewer(), newFakeBinService(), &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})

	req, err := http.NewRequest(http.MethodGet, h.server.URL+"/bins", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("HX-Request", "true")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET /bins (HTMX): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if strings.Contains(string(body), "<layout>") {
		t.Error("HTMX fragment response was wrapped in the layout")
	}
}

func TestBinsWebHandlers_Create_CSRFRejected(t *testing.T) {
	bins := newFakeBinService()
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	h.getCSRF(t, "/bins/new") // establishes the session cookie

	form := url.Values{"csrf_token": {"wrong-token"}, "code": {"A1"}, "name": {"Winter Clothes"}, "location_id": {domain.NewLocationID().String()}, "owner_id": {""}, "visibility": {"public"}}
	resp, _ := h.postForm(t, "/bins", form, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /bins (bad CSRF) = %d, want 403", resp.StatusCode)
	}
	if bins.createCalls != 0 {
		t.Error("Create must not be called when CSRF verification fails")
	}
}

func TestBinsWebHandlers_Create_ValidationRejected_PreservesValues(t *testing.T) {
	bins := newFakeBinService()
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/bins/new")

	form := url.Values{"csrf_token": {csrf}, "code": {"A1"}, "name": {"Winter Clothes"}, "location_id": {"not-a-uuid"}, "owner_id": {""}, "visibility": {"public"}}
	resp, body := h.postForm(t, "/bins", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /bins (bad location) = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Winter Clothes") {
		t.Error("rejected form did not preserve the entered name")
	}
	if bins.createCalls != 0 {
		t.Error("Create must not be called when form validation fails")
	}
}

func TestBinsWebHandlers_Create_Success_Redirects(t *testing.T) {
	bins := newFakeBinService()
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/bins/new")

	form := url.Values{"csrf_token": {csrf}, "code": {"A1"}, "name": {"Winter Clothes"}, "location_id": {domain.NewLocationID().String()}, "owner_id": {""}, "visibility": {"public"}}
	resp, _ := h.postForm(t, "/bins", form, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /bins (valid) = %d, want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/b/A1" {
		t.Errorf("Location = %q, want %q", got, "/b/A1")
	}
	if bins.createCalls != 1 {
		t.Errorf("Create was called %d times, want 1", bins.createCalls)
	}
}

func TestBinsWebHandlers_Detail_NotFound(t *testing.T) {
	h := newBinsWebHarness(t, testViewer(), newFakeBinService(), &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})

	resp, err := h.client.Get(h.server.URL + "/b/GHOST")
	if err != nil {
		t.Fatalf("GET /b/GHOST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /b/GHOST = %d, want 404 (a private bin must appear nowhere, not 403)", resp.StatusCode)
	}
}

func TestBinsWebHandlers_Move_Success_RerendersDetail(t *testing.T) {
	bins := newFakeBinService()
	loc := domain.NewLocationID()
	target := domain.NewLocationID()
	binID := domain.NewBinID()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: binID, Code: "A1", Name: "Winter Clothes", LocationID: loc}})
	mover := &fakeBinMover{}
	locations := &fakeLocationSummaries{summaries: []app.LocationSummary{
		{Location: domain.Location{ID: loc, Name: "Garage"}},
		{Location: domain.Location{ID: target, Name: "Attic"}},
	}}
	h := newBinsWebHarness(t, testViewer(), bins, mover, locations, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/b/A1")

	form := url.Values{"csrf_token": {csrf}, "location_id": {target.String()}}
	resp, body := h.postForm(t, "/bins/"+binID.String()+"/move", form, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST move = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if mover.calls != 1 {
		t.Errorf("Move was called %d times, want 1", mover.calls)
	}
	if !strings.Contains(body, "Winter Clothes") {
		t.Error("move response did not re-render the bin detail fragment")
	}
}
