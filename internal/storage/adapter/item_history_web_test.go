package adapter_test

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// itemHistoryPageSize mirrors adapter.itemHistoryPageSize (unexported, so
// this black-box test package cannot reference it directly) — the timeline's
// own page size, asserted against here rather than duplicated as a magic
// number.
const itemHistoryPageSize = 30

// historyMoreCursorRe extracts the click-to-load button's own "before"
// cursor from a rendered fragment, mirroring csrfRe's own extraction
// pattern (bins_web_test.go).
var historyMoreCursorRe = regexp.MustCompile(`history\?before=([A-Za-z0-9_-]+)`)

// newHistoryEvent builds a minimal, otherwise-valid domain.ItemEvent for
// History/Activity's hermetic tests — the fake event lister never runs
// domain.ItemEvent.Validate, so only the fields a given test actually reads
// need to be set.
func newHistoryEvent(kind domain.EventKind, actorLabel string, occurredAt time.Time) domain.ItemEvent {
	return domain.ItemEvent{
		ID: domain.NewItemEventID(), ItemID: domain.NewItemID(), ItemName: "Camping stove",
		Kind: kind, ActorLabel: actorLabel, OccurredAt: occurredAt, CreatedAt: occurredAt,
	}
}

func TestItemsWebHandlers_History_InBin_RendersCurrentStateAndEvent(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(inBinDetail(id, "BIN-A01"))
	occurredAt := time.Date(2026, 7, 20, 14, 30, 0, 0, time.UTC)
	events := &fakeEventLister{itemEvents: []domain.ItemEvent{
		newHistoryEvent(domain.EventReturned, "Maya", occurredAt),
	}}
	h := newItemsWebHarnessWithPhotosAndEvents(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator(), &fakePrimaryPhotoRefLister{}, events)

	resp, err := h.client.Get(h.server.URL + "/items/" + id.String() + "/history")
	if err != nil {
		t.Fatalf("GET .../history: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `id="item-history"`) {
		t.Errorf("response missing the section's own fragment boundary: %s", body)
	}
	if !strings.Contains(string(body), "In Garage — BIN-A01") {
		t.Errorf("response missing the in-bin current-state headline: %s", body)
	}
	if !strings.Contains(string(body), "Returned to") {
		t.Errorf("response missing the returned event's action sentence: %s", body)
	}
	if !strings.Contains(string(body), `aria-label="Owned by Maya"`) {
		t.Errorf("response missing the event's own actor avatar: %s", body)
	}
	wantTime := occurredAt.Local().Format("3:04 PM")
	if !strings.Contains(string(body), wantTime) {
		t.Errorf("response missing the formatted event time %q: %s", wantTime, body)
	}
	if len(events.itemCalls) != 1 || events.itemCalls[0].Before != nil {
		t.Errorf("ListByItem calls = %+v, want exactly one first-page (Before=nil) call", events.itemCalls)
	}
}

func TestItemsWebHandlers_History_CheckedOut_RendersHolderHeadline(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(checkedOutDetail(id, identity.NewUserID()))
	h := newItemsWebHarnessWithPhotosAndEvents(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator(), &fakePrimaryPhotoRefLister{}, &fakeEventLister{})

	resp, err := h.client.Get(h.server.URL + "/items/" + id.String() + "/history")
	if err != nil {
		t.Fatalf("GET .../history: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Held by Maya since") {
		t.Errorf("response missing the checked-out current-state headline: %s", body)
	}
}

func TestItemsWebHandlers_History_EmptyHistory_RendersEmptyState(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(inBinDetail(id, "BIN-A01"))
	h := newItemsWebHarnessWithPhotosAndEvents(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator(), &fakePrimaryPhotoRefLister{}, &fakeEventLister{})

	resp, err := h.client.Get(h.server.URL + "/items/" + id.String() + "/history")
	if err != nil {
		t.Fatalf("GET .../history: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "No history yet.") {
		t.Errorf("response missing the empty state: %s", body)
	}
}

func TestItemsWebHandlers_History_InvisibleItem_NotFound(t *testing.T) {
	events := &fakeEventLister{}
	h := newItemsWebHarnessWithPhotosAndEvents(t, testViewer(), newFakeItemQueryService(), &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator(), &fakePrimaryPhotoRefLister{}, events)

	resp, err := h.client.Get(h.server.URL + "/items/" + domain.NewItemID().String() + "/history")
	if err != nil {
		t.Fatalf("GET .../history: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET .../history (unknown or invisible item) = %d, want 404", resp.StatusCode)
	}
	if len(events.itemCalls) != 0 {
		t.Error("ListByItem must not be called when the item itself is not visible")
	}
}

func TestItemsWebHandlers_History_BadItemID(t *testing.T) {
	h := newItemsWebHarnessWithPhotosAndEvents(t, testViewer(), newFakeItemQueryService(), &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator(), &fakePrimaryPhotoRefLister{}, &fakeEventLister{})

	resp, err := h.client.Get(h.server.URL + "/items/not-a-uuid/history")
	if err != nil {
		t.Fatalf("GET .../history: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("GET /items/not-a-uuid/history = %d, want 400", resp.StatusCode)
	}
}

// TestItemsWebHandlers_History_MalformedCursor_BadRequest exercises every
// branch parseHistoryCursor can reject on: invalid base64, valid base64 with
// no historyCursorSeparator, an unparseable timestamp, and an unparseable
// event id.
func TestItemsWebHandlers_History_MalformedCursor_BadRequest(t *testing.T) {
	tests := []struct {
		name   string
		cursor string
	}{
		{"invalid base64", "not-valid-base64!!!"},
		{"missing separator", base64.RawURLEncoding.EncodeToString([]byte("no-separator-here"))},
		{"unparseable timestamp", base64.RawURLEncoding.EncodeToString([]byte("not-a-time_" + domain.NewItemEventID().String()))},
		{"unparseable event id", base64.RawURLEncoding.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano) + "_not-a-uuid"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := newFakeItemQueryService()
			id := domain.NewItemID()
			items.addDetail(inBinDetail(id, "BIN-A01"))
			h := newItemsWebHarnessWithPhotosAndEvents(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator(), &fakePrimaryPhotoRefLister{}, &fakeEventLister{})

			resp, err := h.client.Get(h.server.URL + "/items/" + id.String() + "/history?before=" + tt.cursor)
			if err != nil {
				t.Fatalf("GET .../history?before=...: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("GET .../history (%s) = %d, want 400", tt.name, resp.StatusCode)
			}
		})
	}
}

func TestItemsWebHandlers_History_EventListError_Returns500(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(inBinDetail(id, "BIN-A01"))
	events := &fakeEventLister{itemErr: errors.New("boom")}
	h := newItemsWebHarnessWithPhotosAndEvents(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator(), &fakePrimaryPhotoRefLister{}, events)

	resp, err := h.client.Get(h.server.URL + "/items/" + id.String() + "/history")
	if err != nil {
		t.Fatalf("GET .../history: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("GET .../history (event list error) = %d, want 500", resp.StatusCode)
	}
}

func TestItemsWebHandlers_History_FullNavigation_WrapsInLayout(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(inBinDetail(id, "BIN-A01"))
	h := newItemsWebHarnessWithPhotosAndEvents(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator(), &fakePrimaryPhotoRefLister{}, &fakeEventLister{})

	resp, err := h.client.Get(h.server.URL + "/items/" + id.String() + "/history")
	if err != nil {
		t.Fatalf("GET .../history: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<layout>") {
		t.Error("full navigation response was not wrapped in the layout")
	}
}

func TestItemsWebHandlers_History_HTMXRequest_NoLayout(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(inBinDetail(id, "BIN-A01"))
	h := newItemsWebHarnessWithPhotosAndEvents(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator(), &fakePrimaryPhotoRefLister{}, &fakeEventLister{})

	req, err := http.NewRequest(http.MethodGet, h.server.URL+"/items/"+id.String()+"/history", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("HX-Request", "true")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET .../history (HTMX): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "<layout>") {
		t.Error("HTMX fragment response was wrapped in the layout")
	}
}

// TestItemsWebHandlers_History_MoreThanAPage_RendersLoadMoreButton proves
// loadHistoryPage's own "request one extra row" trick: 31 events in means
// exactly 30 render on the first page, plus the click-to-load control.
func TestItemsWebHandlers_History_MoreThanAPage_RendersLoadMoreButton(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(inBinDetail(id, "BIN-A01"))

	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	all := make([]domain.ItemEvent, 31)
	for i := range all {
		all[i] = newHistoryEvent(domain.EventReturned, fmt.Sprintf("Actor%d", i), base.Add(-time.Duration(i)*time.Minute))
	}
	events := &fakeEventLister{itemEvents: all}
	h := newItemsWebHarnessWithPhotosAndEvents(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator(), &fakePrimaryPhotoRefLister{}, events)

	resp, err := h.client.Get(h.server.URL + "/items/" + id.String() + "/history")
	if err != nil {
		t.Fatalf("GET .../history: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	for i := 0; i < 30; i++ {
		if !strings.Contains(string(body), fmt.Sprintf("Actor%d", i)) {
			t.Errorf("response missing event %d of the first 30: %s", i, body)
		}
	}
	if strings.Contains(string(body), "Actor30") {
		t.Error("response rendered the 31st (oldest) event on the first page; the more-row should carry it instead")
	}
	if !strings.Contains(string(body), "Show earlier activity") {
		t.Errorf("response missing the click-to-load control: %s", body)
	}
	if len(events.itemCalls) != 1 || events.itemCalls[0].Limit != itemHistoryPageSize+1 {
		t.Errorf("ListByItem calls = %+v, want one call with Limit %d", events.itemCalls, itemHistoryPageSize+1)
	}
}

// TestItemsWebHandlers_History_SecondPage_ViaCursor proves the click-to-load
// button's own hx-get round-trips through History a second time as a bare
// fragment carrying the next page, never the current-state headline or the
// outer #item-history wrapper (those only ever render on the first page).
func TestItemsWebHandlers_History_SecondPage_ViaCursor(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(inBinDetail(id, "BIN-A01"))

	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	first := make([]domain.ItemEvent, 31)
	for i := range first {
		first[i] = newHistoryEvent(domain.EventReturned, fmt.Sprintf("Actor%d", i), base.Add(-time.Duration(i)*time.Minute))
	}
	events := &fakeEventLister{
		itemEvents:      first,
		itemEventsAfter: []domain.ItemEvent{newHistoryEvent(domain.EventReturned, "OlderActor", base.Add(-time.Hour))},
	}
	h := newItemsWebHarnessWithPhotosAndEvents(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator(), &fakePrimaryPhotoRefLister{}, events)

	firstResp, err := h.client.Get(h.server.URL + "/items/" + id.String() + "/history")
	if err != nil {
		t.Fatalf("GET .../history: %v", err)
	}
	firstBody, _ := io.ReadAll(firstResp.Body)
	_ = firstResp.Body.Close()

	m := historyMoreCursorRe.FindSubmatch(firstBody)
	if m == nil {
		t.Fatalf("no more-row cursor found in first page:\n%s", firstBody)
	}
	cursor := string(m[1])

	resp, err := h.client.Get(h.server.URL + "/items/" + id.String() + "/history?before=" + cursor)
	if err != nil {
		t.Fatalf("GET .../history?before=%s: %v", cursor, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "OlderActor") {
		t.Errorf("second page response missing the next page's event: %s", body)
	}
	if strings.Contains(string(body), `id="item-history"`) {
		t.Error("a ?before page must render a bare fragment, never the outer #item-history section")
	}
	if strings.Contains(string(body), "In Garage") {
		t.Error("a ?before page must not re-render the current-state headline")
	}
	if len(events.itemCalls) != 2 || events.itemCalls[1].Before == nil {
		t.Errorf("ListByItem calls = %+v, want a second call carrying a non-nil Before", events.itemCalls)
	}
}

// TestItemsWebHandlers_History_RendersActionSentencePerKind proves every one
// of the nine EventKind values (Sprint 6's own binding kind list) renders
// its own distinct action sentence.
func TestItemsWebHandlers_History_RendersActionSentencePerKind(t *testing.T) {
	tests := []struct {
		name  string
		event domain.ItemEvent
		want  string
	}{
		{"created", domain.ItemEvent{Kind: domain.EventCreated, ActorLabel: "Maya", OccurredAt: time.Now()}, "Created"},
		{"added", domain.ItemEvent{Kind: domain.EventAdded, ActorLabel: "Maya", BinLabel: "BIN-A01 — Garage Shelf", OccurredAt: time.Now()}, "Added to BIN-A01 — Garage Shelf"},
		{"removed", domain.ItemEvent{Kind: domain.EventRemoved, ActorLabel: "Maya", BinLabel: "BIN-A01 — Garage Shelf", OccurredAt: time.Now()}, "Checked out from BIN-A01 — Garage Shelf"},
		{"returned", domain.ItemEvent{Kind: domain.EventReturned, ActorLabel: "Maya", BinLabel: "BIN-A01 — Garage Shelf", OccurredAt: time.Now()}, "Returned to BIN-A01 — Garage Shelf"},
		{"moved", domain.ItemEvent{Kind: domain.EventMoved, ActorLabel: "Maya", ToLocationLabel: "Pantry", FromLocationLabel: "Hall Closet", OccurredAt: time.Now()}, "Moved to Pantry, previously Hall Closet"},
		{"deleted", domain.ItemEvent{Kind: domain.EventDeleted, ActorLabel: "Maya", OccurredAt: time.Now()}, "Deleted"},
		{"return_requested", domain.ItemEvent{Kind: domain.EventReturnRequested, ActorLabel: "Maya", OccurredAt: time.Now()}, "Requested return"},
		{"return_request_cancelled", domain.ItemEvent{Kind: domain.EventReturnRequestCancelled, ActorLabel: "Maya", OccurredAt: time.Now()}, "Return request cancelled"},
		{"edited single field", domain.ItemEvent{Kind: domain.EventEdited, ActorLabel: "Maya", ChangedFields: []domain.EditedField{domain.FieldName}, OccurredAt: time.Now()}, "Edited the name"},
		{"edited multiple fields", domain.ItemEvent{Kind: domain.EventEdited, ActorLabel: "Maya", ChangedFields: []domain.EditedField{domain.FieldQuantity, domain.FieldName}, OccurredAt: time.Now()}, "Edited the name and quantity"},
		{"edited all three fields", domain.ItemEvent{Kind: domain.EventEdited, ActorLabel: "Maya", ChangedFields: []domain.EditedField{domain.FieldQuantity, domain.FieldDescription, domain.FieldName}, OccurredAt: time.Now()}, "Edited the name, description, and quantity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := newFakeItemQueryService()
			id := domain.NewItemID()
			items.addDetail(inBinDetail(id, "BIN-A01"))
			ev := tt.event
			ev.ID, ev.ItemID = domain.NewItemEventID(), id
			ev.ItemName = "Camping stove"
			events := &fakeEventLister{itemEvents: []domain.ItemEvent{ev}}
			h := newItemsWebHarnessWithPhotosAndEvents(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator(), &fakePrimaryPhotoRefLister{}, events)

			resp, err := h.client.Get(h.server.URL + "/items/" + id.String() + "/history")
			if err != nil {
				t.Fatalf("GET .../history: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
			}
			if !strings.Contains(string(body), tt.want) {
				t.Errorf("%s event response missing %q: %s", tt.name, tt.want, body)
			}
		})
	}
}

// TestItemsWebHandlers_History_EditedEvent_RendersWithoutBinCode proves an
// edited row never carries a bin reference: the item is checked out (so the
// headline itself never mentions a bin either), leaving the em dash
// separator every bin-labeled sentence and the in-bin headline both use as
// the one telltale sign a bin code leaked in.
func TestItemsWebHandlers_History_EditedEvent_RendersWithoutBinCode(t *testing.T) {
	items := newFakeItemQueryService()
	id := domain.NewItemID()
	items.addDetail(checkedOutDetail(id, identity.NewUserID()))
	ev := domain.ItemEvent{
		ID: domain.NewItemEventID(), ItemID: id, ItemName: "Camping stove",
		Kind: domain.EventEdited, ActorLabel: "Maya", ChangedFields: []domain.EditedField{domain.FieldName}, OccurredAt: time.Now(),
	}
	events := &fakeEventLister{itemEvents: []domain.ItemEvent{ev}}
	h := newItemsWebHarnessWithPhotosAndEvents(t, testViewer(), items, &fakeItemOperator{}, &fakeItemBinLister{}, newFakeItemLinkOperator(), &fakePrimaryPhotoRefLister{}, events)

	resp, err := h.client.Get(h.server.URL + "/items/" + id.String() + "/history")
	if err != nil {
		t.Fatalf("GET .../history: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Edited the name") {
		t.Errorf("response missing the edited event's own action sentence: %s", body)
	}
	if strings.Contains(string(body), "—") {
		t.Errorf("response contains a bin-label em dash despite the edited event/checked-out headline carrying no bin: %s", body)
	}
}
