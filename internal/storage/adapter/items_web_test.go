package adapter_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/alexedwards/scs/v2"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/adapter"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// fakeItemQueryService is a configurable itemQueryService fake for
// ItemsWebHandlers' hermetic unit tests, mirroring fakeBinService's own
// shape in bins_web_test.go.
type fakeItemQueryService struct {
	details map[domain.ItemID]domain.ItemDetailResult

	detailErr error
	searchErr error

	searchResults []domain.ItemSearchResult
	searchCalls   []string
}

func newFakeItemQueryService() *fakeItemQueryService {
	return &fakeItemQueryService{details: map[domain.ItemID]domain.ItemDetailResult{}}
}

func (f *fakeItemQueryService) addDetail(result domain.ItemDetailResult) {
	f.details[result.Item.ID] = result
}

func (f *fakeItemQueryService) Detail(_ context.Context, _ identity.Principal, id domain.ItemID) (*domain.ItemDetailResult, error) {
	if f.detailErr != nil {
		return nil, f.detailErr
	}
	result, ok := f.details[id]
	if !ok {
		return nil, domain.ErrItemNotFound
	}
	return &result, nil
}

func (f *fakeItemQueryService) Search(_ context.Context, _ identity.Principal, rawQuery string) ([]domain.ItemSearchResult, error) {
	f.searchCalls = append(f.searchCalls, rawQuery)
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.searchResults, nil
}

// fakeItemOperator is a configurable itemOperator fake driving
// ItemsWebHandlers' check-out/return routes without NSTR-29's real
// transactional service.
type fakeItemOperator struct {
	removeErr error
	returnErr error

	removeCalls int
	returnCalls int
}

func (f *fakeItemOperator) RemoveFromBin(_ context.Context, _ identity.Principal, _ domain.ItemID) (app.Operation, error) {
	f.removeCalls++
	if f.removeErr != nil {
		return app.Operation{}, f.removeErr
	}
	return app.Operation{}, nil
}

func (f *fakeItemOperator) ReturnToBin(_ context.Context, _ identity.Principal, _ domain.ItemID, _ domain.BinID) (app.Operation, error) {
	f.returnCalls++
	if f.returnErr != nil {
		return app.Operation{}, f.returnErr
	}
	return app.Operation{}, nil
}

// fakeItemBinLister is a configurable itemBinLister fake for the return
// control's bin picker.
type fakeItemBinLister struct {
	views []app.BinView
	err   error
}

func (f *fakeItemBinLister) ListVisible(_ context.Context, _ identity.Principal) ([]app.BinView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.views, nil
}

// itemsWebHarness bundles a running ItemsWebHandlers server and a client
// carrying its session cookie across requests, mirroring binsWebHarness'
// own shape.
type itemsWebHarness struct {
	server *httptest.Server
	client *http.Client
	items  *fakeItemQueryService
	ops    *fakeItemOperator
}

func newItemsWebHarness(t *testing.T, viewer identity.Principal, items *fakeItemQueryService, ops *fakeItemOperator, bins *fakeItemBinLister) *itemsWebHarness {
	t.Helper()
	sm := scs.New()
	handlers := adapter.NewItemsWebHandlers(adapter.ItemsWebHandlersDeps{
		Items: items, Operations: ops, Bins: bins, SM: sm, Layout: testLayout, Logger: testLogger(),
	})
	server := newPrincipalServer(t, sm, viewer, handlers.Routes)
	return &itemsWebHarness{server: server, client: newCSRFClient(t), items: items, ops: ops}
}

func (h *itemsWebHarness) getCSRF(t *testing.T, path string) string {
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

func (h *itemsWebHarness) postForm(t *testing.T, path string, form url.Values, htmx bool) (*http.Response, string) {
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

func inBinDetail(id domain.ItemID, binCode string) domain.ItemDetailResult {
	binID := domain.NewBinID()
	return domain.ItemDetailResult{
		Item:         domain.Item{ID: id, Name: "Camping stove", Quantity: 2, CurrentBinID: &binID},
		BinCode:      binCode,
		LocationName: "Garage",
	}
}

func checkedOutDetail(id domain.ItemID, holder identity.UserID) domain.ItemDetailResult {
	return domain.ItemDetailResult{
		Item:       domain.Item{ID: id, Name: "Sleeping bag", Quantity: 1, HeldBy: &holder},
		HolderName: "Maya",
	}
}

func TestNewItemsWebHandlers_NilDependenciesPanic(t *testing.T) {
	items, ops, bins := newFakeItemQueryService(), &fakeItemOperator{}, &fakeItemBinLister{}
	sm := scs.New()
	base := adapter.ItemsWebHandlersDeps{Items: items, Operations: ops, Bins: bins, SM: sm, Layout: testLayout, Logger: testLogger()}

	tests := []struct {
		name   string
		mutate func(*adapter.ItemsWebHandlersDeps)
	}{
		{"nil query service", func(d *adapter.ItemsWebHandlersDeps) { d.Items = nil }},
		{"nil operator", func(d *adapter.ItemsWebHandlersDeps) { d.Operations = nil }},
		{"nil bin lister", func(d *adapter.ItemsWebHandlersDeps) { d.Bins = nil }},
		{"nil session manager", func(d *adapter.ItemsWebHandlersDeps) { d.SM = nil }},
		{"nil layout", func(d *adapter.ItemsWebHandlersDeps) { d.Layout = nil }},
		{"nil logger", func(d *adapter.ItemsWebHandlersDeps) { d.Logger = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewItemsWebHandlers(%s) did not panic", tt.name)
				}
			}()
			deps := base
			tt.mutate(&deps)
			adapter.NewItemsWebHandlers(deps)
		})
	}
}

func TestItemsWebHandlers_Detail_InBin_RendersCheckOutControl(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(inBinDetail(id, "BIN-A01"))
	h := newItemsWebHarness(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{})

	resp, err := h.client.Get(h.server.URL + "/items/" + id.String())
	if err != nil {
		t.Fatalf("GET /items/%s: %v", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Camping stove") || !strings.Contains(string(body), "BIN-A01") {
		t.Errorf("response missing item name/bin code: %s", body)
	}
	if !strings.Contains(string(body), "Check out") {
		t.Errorf("in-bin item response missing the check-out control: %s", body)
	}
	if strings.Contains(string(body), "Return to bin") {
		t.Errorf("in-bin item response should not render the return control: %s", body)
	}
}

func TestItemsWebHandlers_Detail_CheckedOut_RendersHolderSinceAndReturnBins(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(checkedOutDetail(id, identity.NewUserID()))
	binID := domain.NewBinID()
	bins := &fakeItemBinLister{views: []app.BinView{{Bin: domain.Bin{ID: binID, Code: "BIN-A01", Name: "Winter Clothes"}}}}
	h := newItemsWebHarness(t, testViewer(), items, &fakeItemOperator{}, bins)

	resp, err := h.client.Get(h.server.URL + "/items/" + id.String())
	if err != nil {
		t.Fatalf("GET /items/%s: %v", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Maya") {
		t.Errorf("checked-out response missing the holder's name: %s", body)
	}
	if !strings.Contains(string(body), "has held this since") {
		t.Errorf("checked-out response missing the AC's held-since sentence: %s", body)
	}
	if !strings.Contains(string(body), "BIN-A01") {
		t.Errorf("checked-out response missing the return control's bin option: %s", body)
	}
}

func TestItemsWebHandlers_Detail_NotFound(t *testing.T) {
	h := newItemsWebHarness(t, testViewer(), newFakeItemQueryService(), &fakeItemOperator{}, &fakeItemBinLister{})

	resp, err := h.client.Get(h.server.URL + "/items/" + domain.NewItemID().String())
	if err != nil {
		t.Fatalf("GET /items/{unknown}: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /items/{unknown or invisible} = %d, want 404 (never distinguished from unknown)", resp.StatusCode)
	}
}

func TestItemsWebHandlers_Detail_FullNavigation_WrapsInLayout(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(inBinDetail(id, "BIN-A01"))
	h := newItemsWebHarness(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{})

	resp, err := h.client.Get(h.server.URL + "/items/" + id.String())
	if err != nil {
		t.Fatalf("GET /items/%s: %v", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<layout>") {
		t.Error("full navigation response was not wrapped in the layout")
	}
}

func TestItemsWebHandlers_Detail_HTMXRequest_NoLayout(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(inBinDetail(id, "BIN-A01"))
	h := newItemsWebHarness(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{})

	req, err := http.NewRequest(http.MethodGet, h.server.URL+"/items/"+id.String(), nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("HX-Request", "true")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET /items/%s (HTMX): %v", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "<layout>") {
		t.Error("HTMX fragment response was wrapped in the layout")
	}
}

func TestItemsWebHandlers_Detail_BinListError(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(checkedOutDetail(id, identity.NewUserID()))
	bins := &fakeItemBinLister{err: errors.New("boom")}
	h := newItemsWebHarness(t, testViewer(), items, &fakeItemOperator{}, bins)

	resp, err := h.client.Get(h.server.URL + "/items/" + id.String())
	if err != nil {
		t.Fatalf("GET /items/%s: %v", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("GET /items/%s (bin list error) = %d, want 500", id, resp.StatusCode)
	}
}

func TestItemsWebHandlers_Detail_BadID(t *testing.T) {
	h := newItemsWebHarness(t, testViewer(), newFakeItemQueryService(), &fakeItemOperator{}, &fakeItemBinLister{})

	resp, err := h.client.Get(h.server.URL + "/items/not-a-uuid")
	if err != nil {
		t.Fatalf("GET /items/not-a-uuid: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("GET /items/not-a-uuid = %d, want 400", resp.StatusCode)
	}
}

func TestItemsWebHandlers_CheckOut_CSRFRejected(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(inBinDetail(id, "BIN-A01"))
	ops := &fakeItemOperator{}
	h := newItemsWebHarness(t, testViewer(), items, ops, &fakeItemBinLister{})
	h.getCSRF(t, "/items/"+id.String()) // establishes the session cookie

	form := url.Values{"csrf_token": {"wrong-token"}}
	resp, _ := h.postForm(t, "/items/"+id.String()+"/check-out", form, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST .../check-out (bad CSRF) = %d, want 403", resp.StatusCode)
	}
	if ops.removeCalls != 0 {
		t.Error("RemoveFromBin must not be called when CSRF verification fails")
	}
}

func TestItemsWebHandlers_CheckOut_Success_RerendersDetail(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(inBinDetail(id, "BIN-A01"))
	ops := &fakeItemOperator{}
	h := newItemsWebHarness(t, testViewer(), items, ops, &fakeItemBinLister{})
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}}
	resp, body := h.postForm(t, "/items/"+id.String()+"/check-out", form, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST .../check-out = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if ops.removeCalls != 1 {
		t.Errorf("RemoveFromBin called %d times, want 1", ops.removeCalls)
	}
	if !strings.Contains(body, `id="item-detail"`) {
		t.Errorf("check-out response did not re-render the detail fragment: %s", body)
	}
}

func TestItemsWebHandlers_CheckOut_OperationRejected(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(inBinDetail(id, "BIN-A01"))
	ops := &fakeItemOperator{removeErr: domain.ErrHolderRequired}
	h := newItemsWebHarness(t, testViewer(), items, ops, &fakeItemBinLister{})
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}}
	resp, body := h.postForm(t, "/items/"+id.String()+"/check-out", form, true)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST .../check-out (integration principal) = %d, want 403:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Only a signed-in person can check out an item.") {
		t.Errorf("rejected check-out body = %q, want the mapped message", body)
	}
}

func TestItemsWebHandlers_CheckOut_UnmappedError_Returns500(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(inBinDetail(id, "BIN-A01"))
	ops := &fakeItemOperator{removeErr: errors.New("boom")}
	h := newItemsWebHarness(t, testViewer(), items, ops, &fakeItemBinLister{})
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}}
	resp, _ := h.postForm(t, "/items/"+id.String()+"/check-out", form, true)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("POST .../check-out (unmapped error) = %d, want 500", resp.StatusCode)
	}
}

func TestItemsWebHandlers_Return_Success(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(checkedOutDetail(id, identity.NewUserID()))
	ops := &fakeItemOperator{}
	bins := &fakeItemBinLister{views: []app.BinView{{Bin: domain.Bin{ID: domain.NewBinID(), Code: "BIN-A01", Name: "Winter Clothes"}}}}
	h := newItemsWebHarness(t, testViewer(), items, ops, bins)
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}, "bin_id": {domain.NewBinID().String()}}
	resp, body := h.postForm(t, "/items/"+id.String()+"/return", form, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST .../return = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if ops.returnCalls != 1 {
		t.Errorf("ReturnToBin called %d times, want 1", ops.returnCalls)
	}
}

func TestItemsWebHandlers_Return_InvalidBinID_Rejected(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(checkedOutDetail(id, identity.NewUserID()))
	ops := &fakeItemOperator{}
	bins := &fakeItemBinLister{views: []app.BinView{{Bin: domain.Bin{ID: domain.NewBinID(), Code: "BIN-A01", Name: "Winter Clothes"}}}}
	h := newItemsWebHarness(t, testViewer(), items, ops, bins)
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}, "bin_id": {"not-a-uuid"}}
	resp, body := h.postForm(t, "/items/"+id.String()+"/return", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST .../return (bad bin id) = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Please choose a valid bin.") {
		t.Errorf("rejected return body = %q, want the mapped message", body)
	}
	if ops.returnCalls != 0 {
		t.Error("ReturnToBin must not be called when bin_id is invalid")
	}
}

func TestItemsWebHandlers_Return_OperationRejected(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(checkedOutDetail(id, identity.NewUserID()))
	ops := &fakeItemOperator{returnErr: domain.ErrItemNotCheckedOut}
	bins := &fakeItemBinLister{}
	h := newItemsWebHarness(t, testViewer(), items, ops, bins)
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}, "bin_id": {domain.NewBinID().String()}}
	resp, body := h.postForm(t, "/items/"+id.String()+"/return", form, true)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST .../return (not checked out) = %d, want 409:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "This item is not checked out.") {
		t.Errorf("rejected return body = %q, want the mapped message", body)
	}
}

func TestItemsWebHandlers_Return_CSRFRejected(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(checkedOutDetail(id, identity.NewUserID()))
	ops := &fakeItemOperator{}
	h := newItemsWebHarness(t, testViewer(), items, ops, &fakeItemBinLister{})
	h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {"wrong-token"}, "bin_id": {domain.NewBinID().String()}}
	resp, _ := h.postForm(t, "/items/"+id.String()+"/return", form, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST .../return (bad CSRF) = %d, want 403", resp.StatusCode)
	}
	if ops.returnCalls != 0 {
		t.Error("ReturnToBin must not be called when CSRF verification fails")
	}
}

func TestItemsWebHandlers_Search_EmptyQuery_ShowsTypeToSearch(t *testing.T) {
	h := newItemsWebHarness(t, testViewer(), newFakeItemQueryService(), &fakeItemOperator{}, &fakeItemBinLister{})

	resp, err := h.client.Get(h.server.URL + "/search")
	if err != nil {
		t.Fatalf("GET /search: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Type to search.") {
		t.Errorf("GET /search (no query) missing the empty state: %s", body)
	}
}

func TestItemsWebHandlers_Search_WithResults(t *testing.T) {
	items := newFakeItemQueryService()
	items.searchResults = []domain.ItemSearchResult{
		{ID: domain.NewItemID(), Name: "Camping stove", Quantity: 1, State: domain.StateInBin, BinCode: "BIN-A01", LocationName: "Garage"},
	}
	h := newItemsWebHarness(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{})

	resp, err := h.client.Get(h.server.URL + "/search?q=stove")
	if err != nil {
		t.Fatalf("GET /search?q=stove: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Camping stove") {
		t.Errorf("GET /search?q=stove missing the result: %s", body)
	}
	if len(items.searchCalls) != 1 || items.searchCalls[0] != "stove" {
		t.Errorf("Search called with %v, want exactly [\"stove\"]", items.searchCalls)
	}
}

func TestItemsWebHandlers_Search_HTMXRequest_FragmentOnly(t *testing.T) {
	h := newItemsWebHarness(t, testViewer(), newFakeItemQueryService(), &fakeItemOperator{}, &fakeItemBinLister{})

	req, err := http.NewRequest(http.MethodGet, h.server.URL+"/search?q=st", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("HX-Request", "true")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET /search (HTMX): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "<layout>") {
		t.Error("HTMX search fragment was wrapped in the layout")
	}
	if strings.Contains(string(body), "Search &amp; find") {
		t.Error("HTMX search fragment should not include the page heading, only the results div")
	}
	if !strings.Contains(string(body), `id="search-results"`) {
		t.Error("HTMX search fragment missing the results div")
	}
}

func TestItemsWebHandlers_Search_FullNavigation_WrapsInLayout(t *testing.T) {
	h := newItemsWebHarness(t, testViewer(), newFakeItemQueryService(), &fakeItemOperator{}, &fakeItemBinLister{})

	resp, err := h.client.Get(h.server.URL + "/search")
	if err != nil {
		t.Fatalf("GET /search: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<layout>") {
		t.Error("full navigation response was not wrapped in the layout")
	}
	if !strings.Contains(string(body), "Search &amp; find") {
		t.Error("full navigation response missing the page heading")
	}
}

func TestItemsWebHandlers_Search_ServiceError(t *testing.T) {
	items := newFakeItemQueryService()
	items.searchErr = errors.New("boom")
	h := newItemsWebHarness(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{})

	resp, err := h.client.Get(h.server.URL + "/search?q=stove")
	if err != nil {
		t.Fatalf("GET /search?q=stove: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("GET /search (service error) = %d, want 500", resp.StatusCode)
	}
}
