package adapter_test

import (
	"context"
	"errors"
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

// msgInvalidOwnerText and msgInvalidLocationText mirror bins_web.go's own
// unexported msgInvalidOwner/msgInvalidLocation constants — this test
// package (adapter_test) cannot reference them directly.
const (
	msgInvalidOwnerText    = "Please choose a valid owner."
	msgInvalidLocationText = "Please choose a valid location."
)

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

func (f *fakeBinService) Create(_ context.Context, input app.CreateBinInput) (*domain.Bin, error) {
	f.createCalls++
	if f.createErr != nil {
		return nil, f.createErr
	}
	b := domain.Bin{
		ID: domain.NewBinID(), Code: domain.NormalizeBinCode(input.Code), Name: input.Name, Description: input.Description,
		LocationID: input.LocationID, OwnerID: input.OwnerID, Visibility: input.Visibility, CreatedBy: input.CreatedBy,
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

// fakeMembers is a configurable memberLister fake for the bin form's
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
	handlers := adapter.NewBinsWebHandlers(adapter.BinsWebHandlersDeps{
		Bins: bins, Mover: mover, Locations: locations, Members: members, Items: items,
		SM: sm, Layout: testLayout, Logger: testLogger(),
	})
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
	base := adapter.BinsWebHandlersDeps{
		Bins: bins, Mover: mover, Locations: locations, Members: members, Items: items,
		SM: sm, Layout: testLayout, Logger: testLogger(),
	}
	tests := []struct {
		name   string
		mutate func(*adapter.BinsWebHandlersDeps)
	}{
		{"nil bin service", func(d *adapter.BinsWebHandlersDeps) { d.Bins = nil }},
		{"nil mover", func(d *adapter.BinsWebHandlersDeps) { d.Mover = nil }},
		{"nil locations", func(d *adapter.BinsWebHandlersDeps) { d.Locations = nil }},
		{"nil members", func(d *adapter.BinsWebHandlersDeps) { d.Members = nil }},
		{"nil items", func(d *adapter.BinsWebHandlersDeps) { d.Items = nil }},
		{"nil session manager", func(d *adapter.BinsWebHandlersDeps) { d.SM = nil }},
		{"nil layout", func(d *adapter.BinsWebHandlersDeps) { d.Layout = nil }},
		{"nil logger", func(d *adapter.BinsWebHandlersDeps) { d.Logger = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewBinsWebHandlers(%s) did not panic", tt.name)
				}
			}()
			deps := base
			tt.mutate(&deps)
			adapter.NewBinsWebHandlers(deps)
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

func TestBinsWebHandlers_List_BinsServiceError(t *testing.T) {
	bins := newFakeBinService()
	bins.listErr = errors.New("boom")
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})

	resp, err := h.client.Get(h.server.URL + "/bins")
	if err != nil {
		t.Fatalf("GET /bins: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("GET /bins (service error) = %d, want 500", resp.StatusCode)
	}
}

func TestBinsWebHandlers_List_LocationsServiceError(t *testing.T) {
	locations := &fakeLocationSummaries{err: errors.New("boom")}
	h := newBinsWebHarness(t, testViewer(), newFakeBinService(), &fakeBinMover{}, locations, &fakeMembers{}, &fakeItemLister{})

	resp, err := h.client.Get(h.server.URL + "/bins")
	if err != nil {
		t.Fatalf("GET /bins: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("GET /bins (locations error) = %d, want 500", resp.StatusCode)
	}
}

func TestBinsWebHandlers_NewForm_LocationsError(t *testing.T) {
	locations := &fakeLocationSummaries{err: errors.New("boom")}
	h := newBinsWebHarness(t, testViewer(), newFakeBinService(), &fakeBinMover{}, locations, &fakeMembers{}, &fakeItemLister{})

	resp, err := h.client.Get(h.server.URL + "/bins/new")
	if err != nil {
		t.Fatalf("GET /bins/new: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("GET /bins/new (locations error) = %d, want 500", resp.StatusCode)
	}
}

func TestBinsWebHandlers_Create_InvalidOwner_Rejected(t *testing.T) {
	bins := newFakeBinService()
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/bins/new")

	form := url.Values{"csrf_token": {csrf}, "code": {"A1"}, "name": {"Winter Clothes"}, "location_id": {domain.NewLocationID().String()}, "owner_id": {"not-a-uuid"}, "visibility": {"public"}}
	resp, body := h.postForm(t, "/bins", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /bins (bad owner) = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, msgInvalidOwnerText) {
		t.Errorf("rejected form body = %q, want it to contain %q", body, msgInvalidOwnerText)
	}
	if bins.createCalls != 0 {
		t.Error("Create must not be called when the owner is invalid")
	}
}

func TestBinsWebHandlers_Create_InvalidVisibility_Rejected(t *testing.T) {
	bins := newFakeBinService()
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/bins/new")

	form := url.Values{"csrf_token": {csrf}, "code": {"A1"}, "name": {"Winter Clothes"}, "location_id": {domain.NewLocationID().String()}, "owner_id": {""}, "visibility": {"bogus"}}
	resp, body := h.postForm(t, "/bins", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /bins (bad visibility) = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if bins.createCalls != 0 {
		t.Error("Create must not be called when visibility is invalid")
	}
}

func TestBinsWebHandlers_Create_Success_HTMXRedirect(t *testing.T) {
	bins := newFakeBinService()
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/bins/new")

	form := url.Values{"csrf_token": {csrf}, "code": {"A1"}, "name": {"Winter Clothes"}, "location_id": {domain.NewLocationID().String()}, "owner_id": {""}, "visibility": {"public"}}
	resp, _ := h.postForm(t, "/bins", form, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /bins (htmx, valid) = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("HX-Redirect"); got != "/b/A1" {
		t.Errorf("HX-Redirect = %q, want %q", got, "/b/A1")
	}
}

func TestBinsWebHandlers_Create_UnmappedServiceError_Returns500(t *testing.T) {
	bins := newFakeBinService()
	bins.createErr = errors.New("boom")
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/bins/new")

	form := url.Values{"csrf_token": {csrf}, "code": {"A1"}, "name": {"Winter Clothes"}, "location_id": {domain.NewLocationID().String()}, "owner_id": {""}, "visibility": {"public"}}
	resp, _ := h.postForm(t, "/bins", form, false)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("POST /bins (unmapped error) = %d, want 500", resp.StatusCode)
	}
}

func TestBinsWebHandlers_Create_DuplicateCode_Rejected(t *testing.T) {
	bins := newFakeBinService()
	bins.createErr = domain.ErrDuplicateBinCode
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/bins/new")

	form := url.Values{"csrf_token": {csrf}, "code": {"A1"}, "name": {"Winter Clothes"}, "location_id": {domain.NewLocationID().String()}, "owner_id": {""}, "visibility": {"public"}}
	resp, body := h.postForm(t, "/bins", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /bins (duplicate code) = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "already in use") {
		t.Errorf("rejected form body = %q, want the duplicate-code message", body)
	}
}

func TestBinsWebHandlers_Detail_Success_ShowsItemsAndOwner(t *testing.T) {
	bins := newFakeBinService()
	binID := domain.NewBinID()
	bins.addBin(app.BinView{
		Bin:   domain.Bin{ID: binID, Code: "A1", Name: "Winter Clothes"},
		Owner: &app.OwnerInfo{Name: "Maya", Initials: "M", Color: identity.ColorIndigo},
	})
	desc := "Two-burner stove"
	items := &fakeItemLister{items: []domain.Item{
		{Name: "Stove", Description: &desc, Quantity: 1},
		{Name: "Lantern", Quantity: 2},
	}}
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, items)

	resp, err := h.client.Get(h.server.URL + "/b/A1")
	if err != nil {
		t.Fatalf("GET /b/A1: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /b/A1 = %d, want 200:\n%s", resp.StatusCode, body)
	}
	// binItemsList (bindetail.templ) renders only each item's Name and
	// Quantity, never Description — so the description is deliberately not
	// asserted here even though ItemRowView carries it.
	for _, want := range []string{"Winter Clothes", "Maya", "Stove", "Lantern"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("detail body missing %q", want)
		}
	}
}

func TestBinsWebHandlers_EditForm_Success_PrefillsOwner(t *testing.T) {
	bins := newFakeBinService()
	owner := identity.NewUserID()
	bins.addBin(app.BinView{
		Bin:   domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Winter Clothes", Description: "Coats", OwnerID: &owner},
		Owner: &app.OwnerInfo{UserID: owner, Name: "Maya", Initials: "M", Color: identity.ColorIndigo},
	})
	// The owner picker's <option>s come from memberLister.List, not from the
	// bin's own Owner — so the owner must also be seeded as a household
	// member for its option (and thus its id, in the "selected" option's
	// value attribute) to appear in the rendered form at all.
	members := &fakeMembers{users: []identity.User{{ID: owner, DisplayName: "Maya"}}}
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, members, &fakeItemLister{})

	resp, err := h.client.Get(h.server.URL + "/b/A1/edit")
	if err != nil {
		t.Fatalf("GET /b/A1/edit: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /b/A1/edit = %d, want 200:\n%s", resp.StatusCode, body)
	}
	for _, want := range []string{"Winter Clothes", "Coats", owner.String()} {
		if !strings.Contains(string(body), want) {
			t.Errorf("edit form body missing %q", want)
		}
	}
}

func TestBinsWebHandlers_EditForm_NotFound(t *testing.T) {
	h := newBinsWebHarness(t, testViewer(), newFakeBinService(), &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})

	resp, err := h.client.Get(h.server.URL + "/b/GHOST/edit")
	if err != nil {
		t.Fatalf("GET /b/GHOST/edit: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /b/GHOST/edit = %d, want 404", resp.StatusCode)
	}
}

func TestBinsWebHandlers_EditForm_ServiceError_Returns500(t *testing.T) {
	bins := newFakeBinService()
	bins.getByCodeErr = errors.New("boom")
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})

	resp, err := h.client.Get(h.server.URL + "/b/A1/edit")
	if err != nil {
		t.Fatalf("GET /b/A1/edit: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("GET /b/A1/edit (service error) = %d, want 500", resp.StatusCode)
	}
}

func TestBinsWebHandlers_EditForm_FormBuildError_Returns500(t *testing.T) {
	bins := newFakeBinService()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Winter Clothes"}})
	members := &fakeMembers{err: errors.New("boom")}
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, members, &fakeItemLister{})

	resp, err := h.client.Get(h.server.URL + "/b/A1/edit")
	if err != nil {
		t.Fatalf("GET /b/A1/edit: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("GET /b/A1/edit (member directory error) = %d, want 500", resp.StatusCode)
	}
}

func TestBinsWebHandlers_Create_Success_WithOwner(t *testing.T) {
	bins := newFakeBinService()
	owner := identity.NewUserID()
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/bins/new")

	form := url.Values{"csrf_token": {csrf}, "code": {"A1"}, "name": {"Winter Clothes"}, "location_id": {domain.NewLocationID().String()}, "owner_id": {owner.String()}, "visibility": {"public"}}
	resp, _ := h.postForm(t, "/bins", form, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /bins (valid, owned) = %d, want 303", resp.StatusCode)
	}
	if bins.createCalls != 1 {
		t.Errorf("Create was called %d times, want 1", bins.createCalls)
	}
}

func TestBinsWebHandlers_Create_InvalidBin_Rejected(t *testing.T) {
	bins := newFakeBinService()
	bins.createErr = domain.ErrInvalidBin
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/bins/new")

	form := url.Values{"csrf_token": {csrf}, "code": {"A1"}, "name": {"Winter Clothes"}, "location_id": {domain.NewLocationID().String()}, "owner_id": {""}, "visibility": {"public"}}
	resp, body := h.postForm(t, "/bins", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /bins (invalid bin) = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "name and code") {
		t.Errorf("rejected form body = %q, want the invalid-bin message", body)
	}
}

func TestBinsWebHandlers_Create_LocationNotFound_Rejected(t *testing.T) {
	bins := newFakeBinService()
	bins.createErr = domain.ErrLocationNotFound
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/bins/new")

	form := url.Values{"csrf_token": {csrf}, "code": {"A1"}, "name": {"Winter Clothes"}, "location_id": {domain.NewLocationID().String()}, "owner_id": {""}, "visibility": {"public"}}
	resp, body := h.postForm(t, "/bins", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /bins (location vanished) = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, msgInvalidLocationText) {
		t.Errorf("rejected form body = %q, want it to contain %q", body, msgInvalidLocationText)
	}
}

func TestBinsWebHandlers_Create_ValidationRejected_FormBuildError_Returns500(t *testing.T) {
	locations := &fakeLocationSummaries{}
	h := newBinsWebHarness(t, testViewer(), newFakeBinService(), &fakeBinMover{}, locations, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/bins/new")
	// Only start failing locations.List after fetching the CSRF token above
	// (which itself renders /bins/new, and would otherwise fail the same
	// way) — the same *fakeLocationSummaries is shared by reference with
	// the harness, so this reaches buildBinFormView's own call below.
	locations.err = errors.New("boom")

	// A malformed location_id fails parseBinForm's own validation, so
	// Create rejects before ever calling the bin service — the re-render
	// still needs buildBinFormView's location list, which fails here.
	form := url.Values{"csrf_token": {csrf}, "code": {"A1"}, "name": {"Winter Clothes"}, "location_id": {"not-a-uuid"}, "owner_id": {""}, "visibility": {"public"}}
	resp, _ := h.postForm(t, "/bins", form, false)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("POST /bins (rejected form, locations error) = %d, want 500", resp.StatusCode)
	}
}

func TestBinsWebHandlers_Detail_ItemsServiceError_Returns500(t *testing.T) {
	bins := newFakeBinService()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Winter Clothes"}})
	items := &fakeItemLister{err: errors.New("boom")}
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, items)

	resp, err := h.client.Get(h.server.URL + "/b/A1")
	if err != nil {
		t.Fatalf("GET /b/A1: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("GET /b/A1 (items error) = %d, want 500", resp.StatusCode)
	}
}

func TestBinsWebHandlers_Detail_LocationsServiceError_Returns500(t *testing.T) {
	bins := newFakeBinService()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Winter Clothes"}})
	locations := &fakeLocationSummaries{err: errors.New("boom")}
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, locations, &fakeMembers{}, &fakeItemLister{})

	resp, err := h.client.Get(h.server.URL + "/b/A1")
	if err != nil {
		t.Fatalf("GET /b/A1: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("GET /b/A1 (locations error) = %d, want 500", resp.StatusCode)
	}
}

func TestBinsWebHandlers_Update_Success_Redirects(t *testing.T) {
	bins := newFakeBinService()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Winter Clothes"}})
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/b/A1/edit")

	form := url.Values{"csrf_token": {csrf}, "name": {"Summer Clothes"}, "description": {"desc"}, "owner_id": {""}, "visibility": {"public"}}
	resp, _ := h.postForm(t, "/b/A1", form, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /b/A1 (valid) = %d, want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/b/A1" {
		t.Errorf("Location = %q, want %q", got, "/b/A1")
	}
}

func TestBinsWebHandlers_Update_InvalidOwner_Rejected(t *testing.T) {
	bins := newFakeBinService()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Winter Clothes"}})
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/b/A1/edit")

	form := url.Values{"csrf_token": {csrf}, "name": {"Summer Clothes"}, "description": {""}, "owner_id": {"not-a-uuid"}, "visibility": {"public"}}
	resp, body := h.postForm(t, "/b/A1", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /b/A1 (bad owner) = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, msgInvalidOwnerText) {
		t.Errorf("rejected form body = %q, want it to contain %q", body, msgInvalidOwnerText)
	}
}

func TestBinsWebHandlers_Update_InvalidVisibility_Rejected(t *testing.T) {
	bins := newFakeBinService()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Winter Clothes"}})
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/b/A1/edit")

	form := url.Values{"csrf_token": {csrf}, "name": {"Summer Clothes"}, "description": {""}, "owner_id": {""}, "visibility": {"bogus"}}
	resp, body := h.postForm(t, "/b/A1", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /b/A1 (bad visibility) = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Please choose a valid visibility.") {
		t.Errorf("rejected form body = %q, want the invalid-visibility message", body)
	}
}

func TestBinsWebHandlers_Update_NotFound(t *testing.T) {
	h := newBinsWebHarness(t, testViewer(), newFakeBinService(), &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	// CSRF is session-scoped, not resource-scoped (see session.CSRFToken's
	// doc), so a token from any page in the same session verifies here too.
	csrf := h.getCSRF(t, "/bins/new")

	form := url.Values{"csrf_token": {csrf}, "name": {"Name"}, "description": {""}, "owner_id": {""}, "visibility": {"public"}}
	resp, _ := h.postForm(t, "/b/GHOST", form, false)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /b/GHOST (unknown code) = %d, want 404", resp.StatusCode)
	}
}

func TestBinsWebHandlers_Update_ServiceError_Rejected(t *testing.T) {
	bins := newFakeBinService()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Winter Clothes"}})
	bins.editErr = identity.ErrUserNotFound
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/b/A1/edit")

	form := url.Values{"csrf_token": {csrf}, "name": {"Summer Clothes"}, "description": {""}, "owner_id": {""}, "visibility": {"public"}}
	resp, body := h.postForm(t, "/b/A1", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /b/A1 (service error) = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, msgInvalidOwnerText) {
		t.Errorf("rejected form body = %q, want it to contain %q", body, msgInvalidOwnerText)
	}
}

func TestBinsWebHandlers_Update_CSRFRejected(t *testing.T) {
	bins := newFakeBinService()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Winter Clothes"}})
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	h.getCSRF(t, "/b/A1/edit") // establishes the session cookie

	form := url.Values{"csrf_token": {"wrong-token"}, "name": {"Summer Clothes"}, "description": {""}, "owner_id": {""}, "visibility": {"public"}}
	resp, _ := h.postForm(t, "/b/A1", form, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /b/A1 (bad CSRF) = %d, want 403", resp.StatusCode)
	}
}

func TestBinsWebHandlers_Update_UnmappedServiceError_Returns500(t *testing.T) {
	bins := newFakeBinService()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Winter Clothes"}})
	bins.editErr = errors.New("boom")
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/b/A1/edit")

	form := url.Values{"csrf_token": {csrf}, "name": {"Summer Clothes"}, "description": {""}, "owner_id": {""}, "visibility": {"public"}}
	resp, _ := h.postForm(t, "/b/A1", form, false)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("POST /b/A1 (unmapped error) = %d, want 500", resp.StatusCode)
	}
}

func TestBinsWebHandlers_Delete_Success_Redirects(t *testing.T) {
	bins := newFakeBinService()
	binID := domain.NewBinID()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: binID, Code: "A1", Name: "Winter Clothes"}})
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/b/A1")

	resp, _ := h.postForm(t, "/bins/"+binID.String()+"/delete", url.Values{"csrf_token": {csrf}}, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST delete (success) = %d, want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/bins" {
		t.Errorf("Location = %q, want %q", got, "/bins")
	}
}

func TestBinsWebHandlers_Delete_NotEmpty_RerendersDetail(t *testing.T) {
	bins := newFakeBinService()
	binID := domain.NewBinID()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: binID, Code: "A1", Name: "Winter Clothes"}})
	bins.deleteErr = domain.ErrBinNotEmpty
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/b/A1")

	resp, body := h.postForm(t, "/bins/"+binID.String()+"/delete", url.Values{"csrf_token": {csrf}}, true)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST delete (not empty) = %d, want 409:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Winter Clothes") {
		t.Error("rejected delete did not re-render the bin detail fragment")
	}
}

func TestBinsWebHandlers_Delete_NotFound(t *testing.T) {
	h := newBinsWebHarness(t, testViewer(), newFakeBinService(), &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/bins/new")

	resp, _ := h.postForm(t, "/bins/"+domain.NewBinID().String()+"/delete", url.Values{"csrf_token": {csrf}}, false)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST delete (unknown id) = %d, want 404", resp.StatusCode)
	}
}

func TestBinsWebHandlers_Delete_BadID_Returns400(t *testing.T) {
	h := newBinsWebHarness(t, testViewer(), newFakeBinService(), &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/bins/new")

	resp, _ := h.postForm(t, "/bins/not-a-uuid/delete", url.Values{"csrf_token": {csrf}}, false)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST delete (bad id) = %d, want 400", resp.StatusCode)
	}
}

func TestBinsWebHandlers_Delete_CSRFRejected(t *testing.T) {
	bins := newFakeBinService()
	binID := domain.NewBinID()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: binID, Code: "A1", Name: "Winter Clothes"}})
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	h.getCSRF(t, "/b/A1") // establishes the session cookie

	resp, _ := h.postForm(t, "/bins/"+binID.String()+"/delete", url.Values{"csrf_token": {"wrong-token"}}, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST delete (bad CSRF) = %d, want 403", resp.StatusCode)
	}
}

func TestBinsWebHandlers_Delete_UnmappedServiceError_Returns500(t *testing.T) {
	bins := newFakeBinService()
	binID := domain.NewBinID()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: binID, Code: "A1", Name: "Winter Clothes"}})
	bins.deleteErr = errors.New("boom")
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/b/A1")

	resp, _ := h.postForm(t, "/bins/"+binID.String()+"/delete", url.Values{"csrf_token": {csrf}}, false)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("POST delete (unmapped error) = %d, want 500", resp.StatusCode)
	}
}

func TestBinsWebHandlers_Move_InvalidLocation_Rejected(t *testing.T) {
	bins := newFakeBinService()
	binID := domain.NewBinID()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: binID, Code: "A1", Name: "Winter Clothes"}})
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/b/A1")

	form := url.Values{"csrf_token": {csrf}, "location_id": {"not-a-uuid"}}
	resp, body := h.postForm(t, "/bins/"+binID.String()+"/move", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST move (bad location) = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Please choose a valid location.") {
		t.Errorf("rejected move body = %q, want the invalid-location message", body)
	}
}

func TestBinsWebHandlers_Move_ServiceError_Rejected(t *testing.T) {
	bins := newFakeBinService()
	binID := domain.NewBinID()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: binID, Code: "A1", Name: "Winter Clothes"}})
	mover := &fakeBinMover{err: domain.ErrBinAlreadyInLocation}
	h := newBinsWebHarness(t, testViewer(), bins, mover, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/b/A1")

	form := url.Values{"csrf_token": {csrf}, "location_id": {domain.NewLocationID().String()}}
	resp, body := h.postForm(t, "/bins/"+binID.String()+"/move", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST move (service error) = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "already in that location") {
		t.Errorf("rejected move body = %q, want the already-in-location message", body)
	}
}

func TestBinsWebHandlers_Move_BadBinID_Returns400(t *testing.T) {
	h := newBinsWebHarness(t, testViewer(), newFakeBinService(), &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/bins/new")

	form := url.Values{"csrf_token": {csrf}, "location_id": {domain.NewLocationID().String()}}
	resp, _ := h.postForm(t, "/bins/not-a-uuid/move", form, false)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST move (bad id) = %d, want 400", resp.StatusCode)
	}
}

func TestBinsWebHandlers_Move_CSRFRejected(t *testing.T) {
	bins := newFakeBinService()
	binID := domain.NewBinID()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: binID, Code: "A1", Name: "Winter Clothes"}})
	h := newBinsWebHarness(t, testViewer(), bins, &fakeBinMover{}, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	h.getCSRF(t, "/b/A1") // establishes the session cookie

	form := url.Values{"csrf_token": {"wrong-token"}, "location_id": {domain.NewLocationID().String()}}
	resp, _ := h.postForm(t, "/bins/"+binID.String()+"/move", form, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST move (bad CSRF) = %d, want 403", resp.StatusCode)
	}
}

func TestBinsWebHandlers_Move_UnmappedServiceError_Returns500(t *testing.T) {
	bins := newFakeBinService()
	binID := domain.NewBinID()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: binID, Code: "A1", Name: "Winter Clothes"}})
	mover := &fakeBinMover{err: errors.New("boom")}
	h := newBinsWebHarness(t, testViewer(), bins, mover, &fakeLocationSummaries{}, &fakeMembers{}, &fakeItemLister{})
	csrf := h.getCSRF(t, "/b/A1")

	form := url.Values{"csrf_token": {csrf}, "location_id": {domain.NewLocationID().String()}}
	resp, _ := h.postForm(t, "/bins/"+binID.String()+"/move", form, false)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("POST move (unmapped error) = %d, want 500", resp.StatusCode)
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
