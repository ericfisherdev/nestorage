package adapter_test

import (
	"encoding/json"
	"net/http"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/adapter"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

func TestNewBinsAPIHandlers_NilDependenciesPanic(t *testing.T) {
	t.Run("nil bins", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewBinsAPIHandlers(nil, logger) did not panic")
			}
		}()
		adapter.NewBinsAPIHandlers(nil, testLogger())
	})
	t.Run("nil logger", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewBinsAPIHandlers(bins, nil) did not panic")
			}
		}()
		adapter.NewBinsAPIHandlers(newFakeBinService(), nil)
	})
}

func TestBinsAPI_List_ReturnsBins(t *testing.T) {
	bins := newFakeBinService()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Camping", Visibility: domain.VisibilityPublic}, ItemCount: 2})
	handlers := adapter.NewBinsAPIHandlers(bins, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiGet(t, server, "/api/v1/bins")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var body struct {
		Bins []struct {
			Code      string `json:"code"`
			ItemCount int    `json:"item_count"`
		} `json:"bins"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Bins) != 1 || body.Bins[0].Code != "A1" || body.Bins[0].ItemCount != 2 {
		t.Errorf("bins = %+v, want one bin A1 with item_count 2", body.Bins)
	}
}

func TestBinsAPI_List_LocationFilter(t *testing.T) {
	bins := newFakeBinService()
	loc := domain.NewLocationID()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "In location", LocationID: loc, Visibility: domain.VisibilityPublic}})
	bins.addBin(app.BinView{Bin: domain.Bin{ID: domain.NewBinID(), Code: "B1", Name: "Elsewhere", LocationID: domain.NewLocationID(), Visibility: domain.VisibilityPublic}})
	handlers := adapter.NewBinsAPIHandlers(bins, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiGet(t, server, "/api/v1/bins?location="+loc.String())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var body struct {
		Bins []struct {
			Code string `json:"code"`
		} `json:"bins"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Bins) != 1 || body.Bins[0].Code != "A1" {
		t.Errorf("bins(?location=%v) = %+v, want exactly bin A1", loc, body.Bins)
	}
}

func TestBinsAPI_List_MalformedLocationFilterReturns400(t *testing.T) {
	handlers := adapter.NewBinsAPIHandlers(newFakeBinService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiGet(t, server, "/api/v1/bins?location=not-a-uuid")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestBinsAPI_Create_Success_DefaultsVisibilityPublic(t *testing.T) {
	handlers := adapter.NewBinsAPIHandlers(newFakeBinService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiPost(t, server, "/api/v1/bins", `{"code":"a1","name":"Camping","location_id":"`+domain.NewLocationID().String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body decode below", resp.StatusCode, http.StatusCreated)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["code"] != "A1" {
		t.Errorf("code = %v, want normalized %q", body["code"], "A1")
	}
	if body["visibility"] != "public" {
		t.Errorf("visibility = %v, want %q (the default)", body["visibility"], "public")
	}
	if body["item_count"] != float64(0) {
		t.Errorf("item_count = %v, want 0 for a freshly created bin", body["item_count"])
	}
}

func TestBinsAPI_Create_IntegrationPrincipalForbidden(t *testing.T) {
	handlers := adapter.NewBinsAPIHandlers(newFakeBinService(), testLogger())
	server := newAPIPrincipalServer(t, identity.NewIntegrationPrincipal("Nestova"), handlers.Routes)

	resp := apiPost(t, server, "/api/v1/bins", `{"code":"A1","name":"Camping","location_id":"`+domain.NewLocationID().String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	assertErrorCode(t, resp, "forbidden")
}

func TestBinsAPI_Create_BlankCodeReturns422WithFieldDetail(t *testing.T) {
	handlers := adapter.NewBinsAPIHandlers(newFakeBinService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiPost(t, server, "/api/v1/bins", `{"code":"  ","name":"Camping","location_id":"`+domain.NewLocationID().String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	assertFieldDetail(t, resp, "code")
}

func TestBinsAPI_Create_BlankNameReturns422WithFieldDetail(t *testing.T) {
	handlers := adapter.NewBinsAPIHandlers(newFakeBinService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiPost(t, server, "/api/v1/bins", `{"code":"A1","name":"","location_id":"`+domain.NewLocationID().String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	assertFieldDetail(t, resp, "name")
}

func TestBinsAPI_Create_MalformedLocationIDReturns400(t *testing.T) {
	handlers := adapter.NewBinsAPIHandlers(newFakeBinService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiPost(t, server, "/api/v1/bins", `{"code":"A1","name":"Camping","location_id":"not-a-uuid"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	assertFieldDetail(t, resp, "location_id")
}

func TestBinsAPI_Create_UnknownLocationReturns404(t *testing.T) {
	bins := newFakeBinService()
	bins.createErr = domain.ErrLocationNotFound
	handlers := adapter.NewBinsAPIHandlers(bins, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiPost(t, server, "/api/v1/bins", `{"code":"A1","name":"Camping","location_id":"`+domain.NewLocationID().String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestBinsAPI_Create_DuplicateCodeReturns409(t *testing.T) {
	bins := newFakeBinService()
	bins.createErr = domain.ErrDuplicateBinCode
	handlers := adapter.NewBinsAPIHandlers(bins, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiPost(t, server, "/api/v1/bins", `{"code":"A1","name":"Camping","location_id":"`+domain.NewLocationID().String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
	assertErrorCode(t, resp, "conflict")
}

// TestBinsAPI_Get_PrivateBin404sForNonOwner covers the ticket's own AC: a
// non-owner's private bin answers 404, never 403 — fakeBinService.GetByID
// returns domain.ErrBinNotFound unconditionally for any id it does not hold,
// standing in for the real BinRepository's visibility-scoped query.
func TestBinsAPI_Get_PrivateBin404sForNonOwner(t *testing.T) {
	handlers := adapter.NewBinsAPIHandlers(newFakeBinService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiGet(t, server, "/api/v1/bins/"+domain.NewBinID().String())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	assertErrorCode(t, resp, "not_found")
}

func TestBinsAPI_Update_FullReplacementReturnsUpdated(t *testing.T) {
	bins := newFakeBinService()
	owner := identity.NewUserID()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Old", Visibility: domain.VisibilityPublic}})
	var id domain.BinID
	for k := range bins.views {
		id = k
	}
	handlers := adapter.NewBinsAPIHandlers(bins, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	body := `{"name":"New name","description":"updated","owner_id":"` + owner.String() + `","visibility":"private"}`
	resp := apiPut(t, server, "/api/v1/bins/"+id.String(), body)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var respBody map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if respBody["name"] != "New name" || respBody["visibility"] != "private" || respBody["owner_id"] != owner.String() {
		t.Errorf("updated bin = %+v, want name/visibility/owner_id all reflecting the request", respBody)
	}
}

func TestBinsAPI_Update_BlankNameReturns422(t *testing.T) {
	bins := newFakeBinService()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Old", Visibility: domain.VisibilityPublic}})
	var id domain.BinID
	for k := range bins.views {
		id = k
	}
	handlers := adapter.NewBinsAPIHandlers(bins, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiPut(t, server, "/api/v1/bins/"+id.String(), `{"name":"","description":"","visibility":"public"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	assertFieldDetail(t, resp, "name")
}

func TestBinsAPI_Update_InvalidVisibilityReturns422(t *testing.T) {
	bins := newFakeBinService()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Old", Visibility: domain.VisibilityPublic}})
	var id domain.BinID
	for k := range bins.views {
		id = k
	}
	handlers := adapter.NewBinsAPIHandlers(bins, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiPut(t, server, "/api/v1/bins/"+id.String(), `{"name":"Ok","description":"","visibility":"shared"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	assertFieldDetail(t, resp, "visibility")
}

func TestBinsAPI_Delete_Success(t *testing.T) {
	bins := newFakeBinService()
	bins.addBin(app.BinView{Bin: domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Old", Visibility: domain.VisibilityPublic}})
	var id domain.BinID
	for k := range bins.views {
		id = k
	}
	handlers := adapter.NewBinsAPIHandlers(bins, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiDelete(t, server, "/api/v1/bins/"+id.String())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func TestBinsAPI_Delete_NotEmptyReturns409(t *testing.T) {
	bins := newFakeBinService()
	bins.deleteErr = domain.ErrBinNotEmpty
	handlers := adapter.NewBinsAPIHandlers(bins, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiDelete(t, server, "/api/v1/bins/"+domain.NewBinID().String())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
	assertErrorCode(t, resp, "conflict")
}
