package adapter_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// itemLinksHarness bundles an itemsWebHarness whose fake item query service
// already carries one detail row, since every link endpoint's re-render
// goes through renderLinksSection (or, for the full detail page, through
// renderDetail) which — regardless of route — first needs the item itself
// to resolve as visible.
func newItemLinksHarness(t *testing.T) (*itemsWebHarness, domain.ItemID) {
	t.Helper()
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(inBinDetail(id, "BIN-A01"))
	h := newItemsWebHarness(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator())
	return h, id
}

func TestItemsWebHandlers_AddLink_Success_RerendersLinksSection(t *testing.T) {
	h, id := newItemLinksHarness(t)
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}, "label": {"Manual"}, "url": {"https://example.com/manual"}}
	resp, body := h.postForm(t, "/items/"+id.String()+"/links", form, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST .../links = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if h.links.addCalls != 1 {
		t.Errorf("Add called %d times, want 1", h.links.addCalls)
	}
	if !strings.Contains(body, `id="item-links"`) {
		t.Errorf("add response did not re-render the links section fragment: %s", body)
	}
	if !strings.Contains(body, "Manual") || !strings.Contains(body, `href="https://example.com/manual"`) {
		t.Errorf("add response missing the new link: %s", body)
	}
}

func TestItemsWebHandlers_AddLink_InvalidURLRejected(t *testing.T) {
	h, id := newItemLinksHarness(t)
	h.links.addErr = domain.ErrInvalidItemLinkURL
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}, "label": {"Manual"}, "url": {"javascript:alert(1)"}}
	resp, body := h.postForm(t, "/items/"+id.String()+"/links", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST .../links (bad url) = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Only http and https links are allowed.") {
		t.Errorf("rejected add body = %q, want the mapped message", body)
	}
}

func TestItemsWebHandlers_AddLink_BlankLabelRejected(t *testing.T) {
	h, id := newItemLinksHarness(t)
	h.links.addErr = domain.ErrItemLinkLabelRequired
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}, "label": {""}, "url": {"https://example.com"}}
	resp, body := h.postForm(t, "/items/"+id.String()+"/links", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST .../links (blank label) = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Please enter a label.") {
		t.Errorf("rejected add body = %q, want the mapped message", body)
	}
}

func TestItemsWebHandlers_AddLink_ItemNotFound(t *testing.T) {
	h, id := newItemLinksHarness(t)
	h.links.addErr = domain.ErrItemNotFound
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}, "label": {"Manual"}, "url": {"https://example.com"}}
	resp, _ := h.postForm(t, "/items/"+id.String()+"/links", form, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST .../links (invisible item) = %d, want 404", resp.StatusCode)
	}
}

func TestItemsWebHandlers_AddLink_UnmappedError_Returns500(t *testing.T) {
	h, id := newItemLinksHarness(t)
	h.links.addErr = errors.New("boom")
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}, "label": {"Manual"}, "url": {"https://example.com"}}
	resp, _ := h.postForm(t, "/items/"+id.String()+"/links", form, true)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("POST .../links (unmapped error) = %d, want 500", resp.StatusCode)
	}
}

func TestItemsWebHandlers_AddLink_CSRFRejected(t *testing.T) {
	h, id := newItemLinksHarness(t)
	h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {"wrong-token"}, "label": {"Manual"}, "url": {"https://example.com"}}
	resp, _ := h.postForm(t, "/items/"+id.String()+"/links", form, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST .../links (bad CSRF) = %d, want 403", resp.StatusCode)
	}
	if h.links.addCalls != 0 {
		t.Error("Add must not be called when CSRF verification fails")
	}
}

func TestItemsWebHandlers_AddLink_MalformedItemID_400(t *testing.T) {
	h, _ := newItemLinksHarness(t)
	resp, _ := h.postForm(t, "/items/not-a-uuid/links", url.Values{"csrf_token": {"x"}}, false)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /items/not-a-uuid/links = %d, want 400", resp.StatusCode)
	}
}

func TestItemsWebHandlers_EditLink_Success(t *testing.T) {
	h, id := newItemLinksHarness(t)
	csrf := h.getCSRF(t, "/items/"+id.String())

	// Seed a link through Add first, so Edit has a real link id to target.
	addForm := url.Values{"csrf_token": {csrf}, "label": {"Manual"}, "url": {"https://example.com/old"}}
	resp, body := h.postForm(t, "/items/"+id.String()+"/links", addForm, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("seed Add = %d:\n%s", resp.StatusCode, body)
	}
	var linkID string
	for lid := range h.links.links {
		linkID = lid.String()
	}
	if linkID == "" {
		t.Fatal("seed Add did not create a link")
	}

	editForm := url.Values{"csrf_token": {csrf}, "label": {"Owner's manual"}, "url": {"https://example.com/new"}}
	resp, body = h.postForm(t, "/items/"+id.String()+"/links/"+linkID, editForm, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST .../links/%s = %d, want 200:\n%s", linkID, resp.StatusCode, body)
	}
	if h.links.editCalls != 1 {
		t.Errorf("Edit called %d times, want 1", h.links.editCalls)
	}
	if !strings.Contains(body, `href="https://example.com/new"`) {
		t.Errorf("edit response missing the updated url: %s", body)
	}
}

func TestItemsWebHandlers_EditLink_NotFound(t *testing.T) {
	h, id := newItemLinksHarness(t)
	h.links.editErr = domain.ErrItemLinkNotFound
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}, "label": {"Manual"}, "url": {"https://example.com"}}
	resp, body := h.postForm(t, "/items/"+id.String()+"/links/"+domain.NewItemLinkID().String(), form, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST .../links/{unknown} = %d, want 404:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "That link no longer exists.") {
		t.Errorf("rejected edit body = %q, want the mapped message", body)
	}
}

func TestItemsWebHandlers_EditLink_MalformedLinkID_400(t *testing.T) {
	h, id := newItemLinksHarness(t)
	resp, _ := h.postForm(t, "/items/"+id.String()+"/links/not-a-uuid", url.Values{"csrf_token": {"x"}}, false)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /items/{id}/links/not-a-uuid = %d, want 400", resp.StatusCode)
	}
}

func TestItemsWebHandlers_EditLink_CSRFRejected(t *testing.T) {
	h, id := newItemLinksHarness(t)
	h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {"wrong-token"}, "label": {"Manual"}, "url": {"https://example.com"}}
	resp, _ := h.postForm(t, "/items/"+id.String()+"/links/"+domain.NewItemLinkID().String(), form, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST .../links/{id} (bad CSRF) = %d, want 403", resp.StatusCode)
	}
	if h.links.editCalls != 0 {
		t.Error("Edit must not be called when CSRF verification fails")
	}
}

func TestItemsWebHandlers_DeleteLink_Success(t *testing.T) {
	h, id := newItemLinksHarness(t)
	csrf := h.getCSRF(t, "/items/"+id.String())

	// Seed a real link through Add first, so Delete targets a link id the
	// fake actually knows about.
	seedForm := url.Values{"csrf_token": {csrf}, "label": {"Manual"}, "url": {"https://example.com"}}
	resp, body := h.postForm(t, "/items/"+id.String()+"/links", seedForm, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("seed Add = %d:\n%s", resp.StatusCode, body)
	}
	var linkID string
	for lid := range h.links.links {
		linkID = lid.String()
	}
	if linkID == "" {
		t.Fatal("seed Add did not create a link")
	}

	resp, body = h.postForm(t, "/items/"+id.String()+"/links/"+linkID+"/delete", url.Values{"csrf_token": {csrf}}, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST .../links/%s/delete = %d, want 200:\n%s", linkID, resp.StatusCode, body)
	}
	if h.links.removeCalls != 1 {
		t.Errorf("Remove called %d times, want 1", h.links.removeCalls)
	}
	if !strings.Contains(body, `id="item-links"`) {
		t.Errorf("delete response did not re-render the links section fragment: %s", body)
	}
}

func TestItemsWebHandlers_DeleteLink_NotFound(t *testing.T) {
	h, id := newItemLinksHarness(t)
	h.links.removeErr = domain.ErrItemLinkNotFound
	csrf := h.getCSRF(t, "/items/"+id.String())

	resp, body := h.postForm(t, "/items/"+id.String()+"/links/"+domain.NewItemLinkID().String()+"/delete", url.Values{"csrf_token": {csrf}}, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST .../links/{unknown}/delete = %d, want 404:\n%s", resp.StatusCode, body)
	}
}

func TestItemsWebHandlers_DeleteLink_CSRFRejected(t *testing.T) {
	h, id := newItemLinksHarness(t)
	h.getCSRF(t, "/items/"+id.String())

	resp, _ := h.postForm(t, "/items/"+id.String()+"/links/"+domain.NewItemLinkID().String()+"/delete", url.Values{"csrf_token": {"wrong-token"}}, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST .../links/{id}/delete (bad CSRF) = %d, want 403", resp.StatusCode)
	}
	if h.links.removeCalls != 0 {
		t.Error("Remove must not be called when CSRF verification fails")
	}
}

func TestItemsWebHandlers_DeleteLink_MalformedLinkID_400(t *testing.T) {
	h, id := newItemLinksHarness(t)
	resp, _ := h.postForm(t, "/items/"+id.String()+"/links/not-a-uuid/delete", url.Values{"csrf_token": {"x"}}, false)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /items/{id}/links/not-a-uuid/delete = %d, want 400", resp.StatusCode)
	}
}

// TestItemsWebHandlers_Detail_IncludesLinksSection proves renderDetail loads
// and renders the links section alongside the placement panel — the
// reconciled arrangement where a full detail render (including after
// check-out/return) always carries fresh link data since ItemDetail's outer
// div is swapped as a whole on those operations.
func TestItemsWebHandlers_Detail_IncludesLinksSection(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(inBinDetail(id, "BIN-A01"))
	links := newFakeItemLinkOperator()
	if _, err := links.Add(context.Background(), identity.Principal{}, id, "Manual", "https://example.com/manual"); err != nil {
		t.Fatalf("seed link: %v", err)
	}
	h := newItemsWebHarness(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{}, links)

	resp, err := h.client.Get(h.server.URL + "/items/" + id.String())
	if err != nil {
		t.Fatalf("GET /items/%s: %v", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if !strings.Contains(string(body), `id="item-links"`) {
		t.Errorf("detail response missing the links section: %s", body)
	}
	if !strings.Contains(string(body), "Manual") {
		t.Errorf("detail response missing the seeded link: %s", body)
	}
}

// TestItemsWebHandlers_AddLink_XSSPayloadRejectedAndLabelEscaped is the AC's
// end-to-end XSS coverage: a script-tag label paired with a javascript: URL
// must be rejected (the URL never validates), and even the re-rendered
// fragment (which still echoes the rejected label back inside the add
// form's own value, and the item's REAL pre-existing links) never contains
// a raw, unescaped <script> tag.
func TestItemsWebHandlers_AddLink_XSSPayloadRejectedAndLabelEscaped(t *testing.T) {
	h, id := newItemLinksHarness(t)
	h.links.addErr = domain.ErrInvalidItemLinkURL
	csrf := h.getCSRF(t, "/items/"+id.String())

	form := url.Values{"csrf_token": {csrf}, "label": {`<script>alert(1)</script>`}, "url": {"javascript:alert(1)"}}
	resp, body := h.postForm(t, "/items/"+id.String()+"/links", form, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST .../links (xss payload) = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("response rendered an unescaped <script> tag: %s", body)
	}
	if strings.Contains(body, `href="javascript:`) {
		t.Errorf("response rendered a raw javascript: href: %s", body)
	}
	if !strings.Contains(body, "Only http and https links are allowed.") {
		t.Errorf("response missing the rejected-url message: %s", body)
	}
	if h.links.addCalls != 1 {
		t.Errorf("Add called %d times, want 1 (the rejected attempt)", h.links.addCalls)
	}
}
