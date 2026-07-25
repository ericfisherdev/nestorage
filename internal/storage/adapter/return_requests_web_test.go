package adapter_test

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// newReturnRequestsHarness bundles an itemsWebHarness whose fake item query
// service already carries one checked-out detail row held by holder, since
// every return-request endpoint's re-render goes through renderDetail,
// which first needs the item itself to resolve as visible and checked out.
func newReturnRequestsHarness(t *testing.T, viewer identity.Principal, holder identity.UserID) (*itemsWebHarness, domain.ItemID) {
	t.Helper()
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(checkedOutDetail(id, holder))
	h := newItemsWebHarness(t, viewer, items, &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator())
	return h, id
}

func TestItemsWebHandlers_Detail_CheckedOut_NotHolder_ShowsRequestReturnControl(t *testing.T) {
	viewer := testViewer()
	h, id := newReturnRequestsHarness(t, viewer, identity.NewUserID())

	resp, err := h.client.Get(h.server.URL + "/items/" + id.String())
	if err != nil {
		t.Fatalf("GET /items/%s: %v", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if !strings.Contains(string(body), "Request return") {
		t.Errorf("checked-out item viewed by a non-holder should show the request-return control: %s", body)
	}
	if strings.Contains(string(body), "Cancel request") {
		t.Errorf("a viewer with no open request should not see a cancel control: %s", body)
	}
}

// TestItemsWebHandlers_Detail_CheckedOut_Holder_HidesRequestReturnControl
// proves the item's own current holder is never offered a request-return
// control for their own hold.
func TestItemsWebHandlers_Detail_CheckedOut_Holder_HidesRequestReturnControl(t *testing.T) {
	viewer := testViewer()
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(checkedOutDetail(id, viewer.UserID))
	h := newItemsWebHarness(t, viewer, items, &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator())

	resp, err := h.client.Get(h.server.URL + "/items/" + id.String())
	if err != nil {
		t.Fatalf("GET /items/%s: %v", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if strings.Contains(string(body), "Request return") {
		t.Errorf("the item's own holder should never see the request-return control: %s", body)
	}
}

// TestItemsWebHandlers_Detail_CheckedOut_ViewerHasOpenRequest_ShowsCancelControl
// proves the mutually-exclusive other half: a viewer with an open request
// sees the cancel control instead of the request form.
func TestItemsWebHandlers_Detail_CheckedOut_ViewerHasOpenRequest_ShowsCancelControl(t *testing.T) {
	viewer := testViewer()
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(checkedOutDetail(id, identity.NewUserID()))
	returnRequests := newFakeReturnRequestOperator()
	req := domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: id, RequesterID: viewer.UserID, Status: domain.ReturnRequestStatusOpen}
	returnRequests.requests[id] = []domain.ReturnRequest{req}
	h := newItemsWebHarnessFull(t, viewer, items, &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator(), &fakePrimaryPhotoRefLister{}, &fakeEventLister{}, returnRequests)

	resp, err := h.client.Get(h.server.URL + "/items/" + id.String())
	if err != nil {
		t.Fatalf("GET /items/%s: %v", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if !strings.Contains(string(body), "Cancel request") {
		t.Errorf("a viewer with an open request should see the cancel control: %s", body)
	}
	if strings.Contains(string(body), "Request return") {
		t.Errorf("a viewer with an open request should not also see the request form: %s", body)
	}
}

func TestItemsWebHandlers_Detail_InBin_NeverShowsReturnRequestControls(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(inBinDetail(id, "BIN-A01"))
	h := newItemsWebHarness(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator())

	resp, err := h.client.Get(h.server.URL + "/items/" + id.String())
	if err != nil {
		t.Fatalf("GET /items/%s: %v", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if strings.Contains(string(body), "Request return") || strings.Contains(string(body), "Cancel request") {
		t.Errorf("an in-bin item should show neither return-request control: %s", body)
	}
}

func TestItemsWebHandlers_RequestReturn_Success(t *testing.T) {
	viewer := testViewer()
	h, id := newReturnRequestsHarness(t, viewer, identity.NewUserID())
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}, "message": {"please return soon"}}
	resp, body := h.postForm(t, "/items/"+id.String()+"/return-requests", form, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST .../return-requests = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if h.returnRequests.requestCalls != 1 {
		t.Errorf("Request called %d times, want 1", h.returnRequests.requestCalls)
	}
	got := h.returnRequests.requests[id]
	if len(got) != 1 || got[0].Message == nil || *got[0].Message != "please return soon" {
		t.Errorf("Request was not called with the form's message: %+v", got)
	}
	if !strings.Contains(body, "Cancel request") {
		t.Errorf("a successful request should re-render with the cancel control: %s", body)
	}
}

// TestItemsWebHandlers_RequestReturn_BlankMessageOmitted proves an empty
// message field becomes a nil *string, not a pointer to "" — the contract
// domain.ValidateReturnRequestMessage's own doc requires (nil means "no
// message", not "supplied but blank").
func TestItemsWebHandlers_RequestReturn_BlankMessageOmitted(t *testing.T) {
	viewer := testViewer()
	h, id := newReturnRequestsHarness(t, viewer, identity.NewUserID())
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}, "message": {"   "}}
	resp, body := h.postForm(t, "/items/"+id.String()+"/return-requests", form, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST .../return-requests = %d, want 200:\n%s", resp.StatusCode, body)
	}
	got := h.returnRequests.requests[id]
	if len(got) != 1 || got[0].Message != nil {
		t.Errorf("a blank message field should become a nil Message, got %+v", got)
	}
}

func TestItemsWebHandlers_RequestReturn_CSRFRejected(t *testing.T) {
	viewer := testViewer()
	h, id := newReturnRequestsHarness(t, viewer, identity.NewUserID())
	h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {"wrong-token"}}
	resp, _ := h.postForm(t, "/items/"+id.String()+"/return-requests", form, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST .../return-requests (bad CSRF) = %d, want 403", resp.StatusCode)
	}
	if h.returnRequests.requestCalls != 0 {
		t.Error("Request must not be called when CSRF verification fails")
	}
}

func TestItemsWebHandlers_RequestReturn_ErrorMapped(t *testing.T) {
	viewer := testViewer()
	h, id := newReturnRequestsHarness(t, viewer, identity.NewUserID())
	h.returnRequests.requestErr = domain.ErrDuplicateReturnRequest
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}}
	resp, body := h.postForm(t, "/items/"+id.String()+"/return-requests", form, true)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST .../return-requests (duplicate) = %d, want 409:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "You already have an open request for this item.") {
		t.Errorf("rejected request body = %q, want the mapped message", body)
	}
}

func TestItemsWebHandlers_RequestReturn_UnmappedError_Returns500(t *testing.T) {
	viewer := testViewer()
	h, id := newReturnRequestsHarness(t, viewer, identity.NewUserID())
	h.returnRequests.requestErr = errors.New("boom")
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}}
	resp, _ := h.postForm(t, "/items/"+id.String()+"/return-requests", form, true)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("POST .../return-requests (unmapped error) = %d, want 500", resp.StatusCode)
	}
}

func TestItemsWebHandlers_CancelReturnRequest_Success(t *testing.T) {
	viewer := testViewer()
	h, id := newReturnRequestsHarness(t, viewer, identity.NewUserID())
	req := domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: id, RequesterID: viewer.UserID, Status: domain.ReturnRequestStatusOpen}
	h.returnRequests.requests[id] = []domain.ReturnRequest{req}
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}}
	resp, body := h.postForm(t, "/items/"+id.String()+"/return-requests/"+req.ID.String()+"/cancel", form, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST .../cancel = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if h.returnRequests.cancelCalls != 1 {
		t.Errorf("Cancel called %d times, want 1", h.returnRequests.cancelCalls)
	}
	if !strings.Contains(body, "Request return") {
		t.Errorf("a cancelled request should re-render with the request-return control again: %s", body)
	}
}

func TestItemsWebHandlers_CancelReturnRequest_CSRFRejected(t *testing.T) {
	viewer := testViewer()
	h, id := newReturnRequestsHarness(t, viewer, identity.NewUserID())
	req := domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: id, RequesterID: viewer.UserID, Status: domain.ReturnRequestStatusOpen}
	h.returnRequests.requests[id] = []domain.ReturnRequest{req}
	h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {"wrong-token"}}
	resp, _ := h.postForm(t, "/items/"+id.String()+"/return-requests/"+req.ID.String()+"/cancel", form, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST .../cancel (bad CSRF) = %d, want 403", resp.StatusCode)
	}
	if h.returnRequests.cancelCalls != 0 {
		t.Error("Cancel must not be called when CSRF verification fails")
	}
}

func TestItemsWebHandlers_CancelReturnRequest_NotFoundMapped(t *testing.T) {
	viewer := testViewer()
	h, id := newReturnRequestsHarness(t, viewer, identity.NewUserID())
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}}
	resp, body := h.postForm(t, "/items/"+id.String()+"/return-requests/"+domain.NewReturnRequestID().String()+"/cancel", form, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST .../cancel (unknown request) = %d, want 404:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "That request no longer exists.") {
		t.Errorf("rejected cancel body = %q, want the mapped message", body)
	}
}

func TestItemsWebHandlers_CancelReturnRequest_BadRequestID(t *testing.T) {
	viewer := testViewer()
	h, id := newReturnRequestsHarness(t, viewer, identity.NewUserID())
	h.getCSRF(t, "/items/"+id.String())

	resp, err := h.client.Post(h.server.URL+"/items/"+id.String()+"/return-requests/not-a-uuid/cancel", "application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST .../cancel (bad request id): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST .../cancel (bad request id) = %d, want 400", resp.StatusCode)
	}
	if h.returnRequests.cancelCalls != 0 {
		t.Error("Cancel must not be called for a malformed request id")
	}
}
