package adapter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/adapter"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// fakeBinDetailReader is a configurable binDetailReader fake for
// HistoryAPIHandlers' hermetic unit tests.
type fakeBinDetailReader struct {
	result *app.BinView
	err    error
	calls  int
}

func (f *fakeBinDetailReader) GetByID(_ context.Context, _ identity.Principal, _ domain.BinID) (*app.BinView, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

// historyAPIFakes bundles HistoryAPIHandlers' four dependencies so each test
// only names the ones it configures. events (fakeEventLister,
// web_common_test.go) already satisfies both itemEventLister and
// binActivityLister — the same concrete fake ItemsWebHandlers/
// BinsWebHandlers' own tests already share, reused here rather than
// duplicated.
type historyAPIFakes struct {
	items  *fakeItemDetailReader
	events *fakeEventLister
	bins   *fakeBinDetailReader
}

func newHistoryAPIFakes() *historyAPIFakes {
	return &historyAPIFakes{items: &fakeItemDetailReader{}, events: &fakeEventLister{}, bins: &fakeBinDetailReader{}}
}

func (f *historyAPIFakes) handlers() *adapter.HistoryAPIHandlers {
	return adapter.NewHistoryAPIHandlers(f.items, f.events, f.bins, f.events, testLogger())
}

func TestNewHistoryAPIHandlers_NilDependenciesPanic(t *testing.T) {
	cases := map[string]func(f *historyAPIFakes) func(){
		"nil items": func(f *historyAPIFakes) func() {
			return func() { adapter.NewHistoryAPIHandlers(nil, f.events, f.bins, f.events, testLogger()) }
		},
		"nil item events": func(f *historyAPIFakes) func() {
			return func() { adapter.NewHistoryAPIHandlers(f.items, nil, f.bins, f.events, testLogger()) }
		},
		"nil bins": func(f *historyAPIFakes) func() {
			return func() { adapter.NewHistoryAPIHandlers(f.items, f.events, nil, f.events, testLogger()) }
		},
		"nil bin events": func(f *historyAPIFakes) func() {
			return func() { adapter.NewHistoryAPIHandlers(f.items, f.events, f.bins, nil, testLogger()) }
		},
		"nil logger": func(f *historyAPIFakes) func() {
			return func() { adapter.NewHistoryAPIHandlers(f.items, f.events, f.bins, f.events, nil) }
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: did not panic", name)
				}
			}()
			build(newHistoryAPIFakes())()
		})
	}
}

// eventFor builds a Validate-passing domain.ItemEvent attributed to actor,
// via domain.NewItemEvent, so ActorKind/ActorLabel/ActorUserID are wired
// exactly as production code sets them (AC 3's own subject) — distinct from
// item_history_web_test.go's own newHistoryEvent, which leaves those three
// fields zero since ItemsWebHandlers' tests never assert on them.
func eventFor(actor identity.Principal, kind domain.EventKind, occurredAt time.Time) domain.ItemEvent {
	e := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Camping stove", kind, actor)
	e.OccurredAt, e.CreatedAt = occurredAt, occurredAt
	return e
}

func TestHistoryAPI_ItemHistory_Success(t *testing.T) {
	f := newHistoryAPIFakes()
	itemID := domain.NewItemID()
	viewer := testViewer()
	f.items.result = &domain.ItemDetailResult{Item: testItem(itemID, nil, &viewer.UserID)}
	binID := domain.NewBinID()
	added := eventFor(viewer, domain.EventAdded, time.Now())
	added.BinID, added.BinLabel = &binID, "BIN-A01 — Pantry shelf"
	f.events.itemEvents = []domain.ItemEvent{added}
	server := newAPIPrincipalServer(t, viewer, f.handlers().Routes)

	resp := apiGet(t, server, "/api/v1/items/"+itemID.String()+"/history")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var body struct {
		Events []struct {
			Kind  string `json:"kind"`
			Actor struct {
				Kind   string  `json:"kind"`
				Label  string  `json:"label"`
				UserID *string `json:"user_id"`
			} `json:"actor"`
			Bin *struct {
				ID    string `json:"id"`
				Label string `json:"label"`
			} `json:"bin"`
			ChangedFields []string `json:"changed_fields"`
			Item          struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"item"`
		} `json:"events"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Events) != 1 {
		t.Fatalf("events = %+v, want 1", body.Events)
	}
	got := body.Events[0]
	if got.Kind != "added" || got.Bin == nil || got.Bin.ID != binID.String() {
		t.Errorf("event = %+v, want kind=added with bin %v", got, binID)
	}
	if got.Actor.Kind != "user" || got.Actor.Label != viewer.Label || got.Actor.UserID == nil || *got.Actor.UserID != viewer.UserID.String() {
		t.Errorf("actor = %+v, want user %v (%v)", got.Actor, viewer.UserID, viewer.Label)
	}
	if got.ChangedFields == nil {
		t.Error("changed_fields = nil, want an empty (never null) array for a non-edited event")
	}
	if got.Item.ID != added.ItemID.String() || got.Item.Name != "Camping stove" {
		t.Errorf("item = %+v, want the event's own item snapshot", got.Item)
	}
	if body.NextCursor != nil {
		t.Errorf("next_cursor = %v, want nil (only 1 event, well under the page limit)", *body.NextCursor)
	}
}

// TestHistoryAPI_ItemHistory_IntegrationActorHasNullUserID proves AC 3:
// history responses include the actor for both principal kinds, the
// integration principal's own user_id rendering null since it has no
// person behind it.
func TestHistoryAPI_ItemHistory_IntegrationActorHasNullUserID(t *testing.T) {
	f := newHistoryAPIFakes()
	itemID := domain.NewItemID()
	viewer := testViewer()
	f.items.result = &domain.ItemDetailResult{Item: testItem(itemID, nil, &viewer.UserID)}
	f.events.itemEvents = []domain.ItemEvent{eventFor(identity.NewIntegrationPrincipal("Nestova"), domain.EventCreated, time.Now())}
	server := newAPIPrincipalServer(t, viewer, f.handlers().Routes)

	resp := apiGet(t, server, "/api/v1/items/"+itemID.String()+"/history")
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Events []struct {
			Actor struct {
				Kind   string  `json:"kind"`
				UserID *string `json:"user_id"`
			} `json:"actor"`
		} `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Events) != 1 || body.Events[0].Actor.Kind != "integration" || body.Events[0].Actor.UserID != nil {
		t.Errorf("actor = %+v, want kind=integration with a null user_id", body.Events)
	}
}

func TestHistoryAPI_ItemHistory_InvisibleItem_Returns404WithoutListingEvents(t *testing.T) {
	f := newHistoryAPIFakes()
	f.items.err = domain.ErrItemNotFound
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiGet(t, server, "/api/v1/items/"+domain.NewItemID().String()+"/history")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	assertErrorCode(t, resp, "item_not_found")
	if len(f.events.itemCalls) != 0 {
		t.Errorf("ListByItem calls = %d, want 0 (visibility must be checked before any event read)", len(f.events.itemCalls))
	}
}

func TestHistoryAPI_ItemHistory_MalformedCursor_Returns400(t *testing.T) {
	f := newHistoryAPIFakes()
	itemID := domain.NewItemID()
	f.items.result = &domain.ItemDetailResult{Item: testItem(itemID, nil, nil)}
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiGet(t, server, "/api/v1/items/"+itemID.String()+"/history?before=%25not-a-cursor%25")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	assertFieldDetail(t, resp, "before")
}

// TestHistoryAPI_ItemHistory_MoreThanAPage_ReportsNextCursor proves
// loadHistoryEventPage's own "request one extra row" trick: 3 events in
// with ?limit=2 trims to 2 and reports a non-nil next_cursor.
func TestHistoryAPI_ItemHistory_MoreThanAPage_ReportsNextCursor(t *testing.T) {
	f := newHistoryAPIFakes()
	itemID := domain.NewItemID()
	viewer := testViewer()
	f.items.result = &domain.ItemDetailResult{Item: testItem(itemID, nil, &viewer.UserID)}
	base := time.Now()
	f.events.itemEvents = []domain.ItemEvent{
		eventFor(viewer, domain.EventCreated, base),
		eventFor(viewer, domain.EventCreated, base.Add(-time.Minute)),
		eventFor(viewer, domain.EventCreated, base.Add(-2*time.Minute)),
	}
	server := newAPIPrincipalServer(t, viewer, f.handlers().Routes)

	resp := apiGet(t, server, "/api/v1/items/"+itemID.String()+"/history?limit=2")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var body struct {
		Events     []json.RawMessage `json:"events"`
		NextCursor *string           `json:"next_cursor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Events) != 2 {
		t.Errorf("events = %d, want 2 (trimmed to the requested limit)", len(body.Events))
	}
	if body.NextCursor == nil {
		t.Error("next_cursor = nil, want a cursor (3 events in in, over the limit of 2)")
	}
	if len(f.events.itemCalls) != 1 || f.events.itemCalls[0].Limit != 3 {
		t.Errorf("ListByItem calls = %+v, want one call with Limit 3 (requested limit + 1)", f.events.itemCalls)
	}
}

func TestHistoryAPI_BinHistory_Success(t *testing.T) {
	f := newHistoryAPIFakes()
	binID := domain.NewBinID()
	viewer := testViewer()
	f.bins.result = &app.BinView{Bin: domain.Bin{ID: binID, Code: "BIN-A01", Name: "Pantry shelf", LocationID: domain.NewLocationID()}}
	fromLoc, toLoc := domain.NewLocationID(), domain.NewLocationID()
	moved := eventFor(viewer, domain.EventMoved, time.Now())
	moved.BinID, moved.BinLabel = &binID, "BIN-A01 — Pantry shelf"
	moved.FromLocationID, moved.FromLocationLabel = &fromLoc, "Hall closet"
	moved.ToLocationID, moved.ToLocationLabel = &toLoc, "Pantry"
	f.events.binEvents = []domain.ItemEvent{moved}
	server := newAPIPrincipalServer(t, viewer, f.handlers().Routes)

	resp := apiGet(t, server, "/api/v1/bins/"+binID.String()+"/history")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var body struct {
		Events []struct {
			Kind         string `json:"kind"`
			FromLocation *struct {
				ID    string `json:"id"`
				Label string `json:"label"`
			} `json:"from_location"`
			ToLocation *struct {
				ID    string `json:"id"`
				Label string `json:"label"`
			} `json:"to_location"`
		} `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Events) != 1 {
		t.Fatalf("events = %+v, want 1", body.Events)
	}
	got := body.Events[0]
	if got.Kind != "moved" || got.FromLocation == nil || got.FromLocation.ID != fromLoc.String() || got.ToLocation == nil || got.ToLocation.ID != toLoc.String() {
		t.Errorf("event = %+v, want kind=moved with from/to location %v/%v", got, fromLoc, toLoc)
	}
	if f.bins.calls != 1 {
		t.Errorf("GetByID calls = %d, want 1 (visibility must be checked before any event read)", f.bins.calls)
	}
}

func TestHistoryAPI_BinHistory_InvisibleBin_Returns404WithoutListingEvents(t *testing.T) {
	f := newHistoryAPIFakes()
	f.bins.err = domain.ErrBinNotFound
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiGet(t, server, "/api/v1/bins/"+domain.NewBinID().String()+"/history")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	assertErrorCode(t, resp, "bin_not_found")
	if f.events.binCalls != 0 {
		t.Errorf("ListByBin calls = %d, want 0 (visibility must be checked before any event read)", f.events.binCalls)
	}
}
