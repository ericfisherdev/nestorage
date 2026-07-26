package adapter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/adapter"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// fakeItemAPIService is a configurable itemAPIService fake for
// ItemsAPIHandlers' hermetic unit tests, mirroring fakeBinService/
// fakeLocationService's own shape (bins_web_test.go/locations_web_test.go).
type fakeItemAPIService struct {
	items map[domain.ItemID]domain.Item

	getErr    error
	listErr   error
	createErr error
	editErr   error
	deleteErr error
}

func newFakeItemAPIService() *fakeItemAPIService {
	return &fakeItemAPIService{items: map[domain.ItemID]domain.Item{}}
}

func (f *fakeItemAPIService) addItem(it domain.Item) { f.items[it.ID] = it }

func (f *fakeItemAPIService) Get(_ context.Context, _ identity.Principal, id domain.ItemID) (*domain.Item, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	it, ok := f.items[id]
	if !ok {
		return nil, domain.ErrItemNotFound
	}
	return &it, nil
}

func (f *fakeItemAPIService) ListVisible(_ context.Context, _ identity.Principal, filter domain.ItemFilter) ([]domain.Item, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	items := make([]domain.Item, 0, len(f.items))
	for _, it := range f.items {
		if filter.BinID != nil && (it.CurrentBinID == nil || *it.CurrentBinID != *filter.BinID) {
			continue
		}
		if filter.HeldBy != nil && (it.HeldBy == nil || *it.HeldBy != *filter.HeldBy) {
			continue
		}
		items = append(items, it)
	}
	// Ordered by name, tie-broken by id — matching
	// domain.ItemRepository.ListVisible's own documented order, which
	// items_api_test.go's pagination tests depend on.
	slices.SortFunc(items, func(a, b domain.Item) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.ID.String(), b.ID.String())
	})
	return items, nil
}

func (f *fakeItemAPIService) Create(_ context.Context, name string, description *string, quantity int, binID domain.BinID, actor identity.Principal) (*domain.Item, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	if strings.TrimSpace(name) == "" {
		return nil, domain.ErrItemNameRequired
	}
	if quantity <= 0 {
		return nil, domain.ErrInvalidQuantity
	}
	now := time.Now()
	it := domain.Item{
		ID: domain.NewItemID(), Name: name, Description: description, Quantity: quantity,
		CurrentBinID: &binID, CreatedBy: actor.UserID,
		PlacementChangedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	f.addItem(it)
	return &it, nil
}

func (f *fakeItemAPIService) Edit(_ context.Context, id domain.ItemID, name string, description *string, quantity int, _ identity.Principal) error {
	if f.editErr != nil {
		return f.editErr
	}
	it, ok := f.items[id]
	if !ok {
		return domain.ErrItemNotFound
	}
	if strings.TrimSpace(name) == "" {
		return domain.ErrItemNameRequired
	}
	if quantity <= 0 {
		return domain.ErrInvalidQuantity
	}
	it.Name, it.Description, it.Quantity = name, description, quantity
	f.items[id] = it
	return nil
}

func (f *fakeItemAPIService) Delete(_ context.Context, id domain.ItemID, _ identity.Principal) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.items[id]; !ok {
		return domain.ErrItemNotFound
	}
	delete(f.items, id)
	return nil
}

func TestNewItemsAPIHandlers_NilDependenciesPanic(t *testing.T) {
	t.Run("nil items", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewItemsAPIHandlers(nil, logger) did not panic")
			}
		}()
		adapter.NewItemsAPIHandlers(nil, testLogger())
	})
	t.Run("nil logger", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewItemsAPIHandlers(items, nil) did not panic")
			}
		}()
		adapter.NewItemsAPIHandlers(newFakeItemAPIService(), nil)
	})
}

func TestItemsAPI_List_ReturnsItemsWithState(t *testing.T) {
	items := newFakeItemAPIService()
	binID := domain.NewBinID()
	items.addItem(domain.Item{ID: domain.NewItemID(), Name: "Stove", Quantity: 1, CurrentBinID: &binID, CreatedAt: time.Now(), UpdatedAt: time.Now(), PlacementChangedAt: time.Now()})
	handlers := adapter.NewItemsAPIHandlers(items, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiGet(t, server, "/api/v1/items")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var body struct {
		Items []struct {
			Name  string `json:"name"`
			State string `json:"state"`
			BinID string `json:"bin_id"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].Name != "Stove" || body.Items[0].State != "in_bin" || body.Items[0].BinID != binID.String() {
		t.Errorf("items = %+v, want one in_bin item in %v", body.Items, binID)
	}
}

func TestItemsAPI_List_HolderFilter(t *testing.T) {
	items := newFakeItemAPIService()
	holder := identity.NewUserID()
	binID := domain.NewBinID()
	items.addItem(domain.Item{ID: domain.NewItemID(), Name: "Held", Quantity: 1, HeldBy: &holder, CreatedAt: time.Now(), UpdatedAt: time.Now(), PlacementChangedAt: time.Now()})
	items.addItem(domain.Item{ID: domain.NewItemID(), Name: "In bin", Quantity: 1, CurrentBinID: &binID, CreatedAt: time.Now(), UpdatedAt: time.Now(), PlacementChangedAt: time.Now()})
	handlers := adapter.NewItemsAPIHandlers(items, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiGet(t, server, "/api/v1/items?holder="+holder.String())
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Items []struct {
			Name   string  `json:"name"`
			State  string  `json:"state"`
			HeldBy *string `json:"held_by"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].Name != "Held" || body.Items[0].State != "checked_out" {
		t.Errorf("items(?holder=%v) = %+v, want exactly the held item", holder, body.Items)
	}
}

func TestItemsAPI_List_MalformedBinFilterReturns400(t *testing.T) {
	handlers := adapter.NewItemsAPIHandlers(newFakeItemAPIService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiGet(t, server, "/api/v1/items?bin=not-a-uuid")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	assertFieldDetail(t, resp, "bin")
}

func TestItemsAPI_Create_Success(t *testing.T) {
	handlers := adapter.NewItemsAPIHandlers(newFakeItemAPIService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)
	binID := domain.NewBinID()

	resp := apiPost(t, server, "/api/v1/items", `{"name":"Stove","quantity":1,"bin_id":"`+binID.String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["name"] != "Stove" || body["state"] != "in_bin" || body["bin_id"] != binID.String() {
		t.Errorf("created item = %+v, want name/state/bin_id reflecting the request", body)
	}
}

func TestItemsAPI_Create_IntegrationPrincipalForbidden(t *testing.T) {
	handlers := adapter.NewItemsAPIHandlers(newFakeItemAPIService(), testLogger())
	server := newAPIPrincipalServer(t, identity.NewIntegrationPrincipal("Nestova"), handlers.Routes)

	resp := apiPost(t, server, "/api/v1/items", `{"name":"Stove","quantity":1,"bin_id":"`+domain.NewBinID().String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	assertErrorCode(t, resp, "forbidden")
}

func TestItemsAPI_Create_InvalidQuantityReturns422WithFieldDetail(t *testing.T) {
	handlers := adapter.NewItemsAPIHandlers(newFakeItemAPIService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiPost(t, server, "/api/v1/items", `{"name":"Stove","quantity":0,"bin_id":"`+domain.NewBinID().String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	assertFieldDetail(t, resp, "quantity")
}

func TestItemsAPI_Create_MalformedBinIDReturns400(t *testing.T) {
	handlers := adapter.NewItemsAPIHandlers(newFakeItemAPIService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiPost(t, server, "/api/v1/items", `{"name":"Stove","quantity":1,"bin_id":"not-a-uuid"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	assertFieldDetail(t, resp, "bin_id")
}

func TestItemsAPI_Get_NotFound(t *testing.T) {
	handlers := adapter.NewItemsAPIHandlers(newFakeItemAPIService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiGet(t, server, "/api/v1/items/"+domain.NewItemID().String())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestItemsAPI_Update_AuthorizesByReadFirst(t *testing.T) {
	items := newFakeItemAPIService()
	items.getErr = domain.ErrItemNotFound
	handlers := adapter.NewItemsAPIHandlers(items, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiPut(t, server, "/api/v1/items/"+domain.NewItemID().String(), `{"name":"New","quantity":1}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d (Get must run before Edit)", resp.StatusCode, http.StatusNotFound)
	}
}

func TestItemsAPI_Update_Success(t *testing.T) {
	items := newFakeItemAPIService()
	binID := domain.NewBinID()
	it := domain.Item{ID: domain.NewItemID(), Name: "Old", Quantity: 1, CurrentBinID: &binID, CreatedAt: time.Now(), UpdatedAt: time.Now(), PlacementChangedAt: time.Now()}
	items.addItem(it)
	handlers := adapter.NewItemsAPIHandlers(items, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiPut(t, server, "/api/v1/items/"+it.ID.String(), `{"name":"New name","quantity":3}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["name"] != "New name" || body["quantity"] != float64(3) {
		t.Errorf("updated item = %+v, want name/quantity reflecting the request", body)
	}
}

func TestItemsAPI_Delete_AuthorizesByReadFirst(t *testing.T) {
	items := newFakeItemAPIService()
	items.getErr = domain.ErrItemNotFound
	handlers := adapter.NewItemsAPIHandlers(items, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiDelete(t, server, "/api/v1/items/"+domain.NewItemID().String())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d (Get must run before Delete)", resp.StatusCode, http.StatusNotFound)
	}
}

func TestItemsAPI_Delete_Success(t *testing.T) {
	items := newFakeItemAPIService()
	binID := domain.NewBinID()
	it := domain.Item{ID: domain.NewItemID(), Name: "Old", Quantity: 1, CurrentBinID: &binID, CreatedAt: time.Now(), UpdatedAt: time.Now(), PlacementChangedAt: time.Now()}
	items.addItem(it)
	handlers := adapter.NewItemsAPIHandlers(items, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiDelete(t, server, "/api/v1/items/"+it.ID.String())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}
