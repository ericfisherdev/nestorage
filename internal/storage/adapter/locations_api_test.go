package adapter_test

import (
	"encoding/json"
	"net/http"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/adapter"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

func TestNewLocationsAPIHandlers_NilDependenciesPanic(t *testing.T) {
	t.Run("nil locations", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewLocationsAPIHandlers(nil, logger) did not panic")
			}
		}()
		adapter.NewLocationsAPIHandlers(nil, testLogger())
	})
	t.Run("nil logger", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewLocationsAPIHandlers(locations, nil) did not panic")
			}
		}()
		adapter.NewLocationsAPIHandlers(newFakeLocationService(), nil)
	})
}

func TestLocationsAPI_List_ReturnsBinCountEnrichedLocations(t *testing.T) {
	locations := newFakeLocationService()
	l := domain.Location{ID: domain.NewLocationID(), Name: "Garage"}
	locations.locations[l.ID] = l
	locations.binCounts = map[domain.LocationID]int{l.ID: 3}

	handlers := adapter.NewLocationsAPIHandlers(locations, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiGet(t, server, "/api/v1/locations")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body struct {
		Locations []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			BinCount int    `json:"bin_count"`
		} `json:"locations"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Locations) != 1 || body.Locations[0].ID != l.ID.String() || body.Locations[0].BinCount != 3 {
		t.Errorf("locations = %+v, want one entry for %v with bin_count 3", body.Locations, l.ID)
	}
	if body.NextCursor != nil {
		t.Errorf("next_cursor = %v, want nil (single page)", body.NextCursor)
	}
}

func TestLocationsAPI_Create_Success(t *testing.T) {
	handlers := adapter.NewLocationsAPIHandlers(newFakeLocationService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiPost(t, server, "/api/v1/locations", `{"name":"Garage","description":"Main garage"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["id"].(string); !ok {
		t.Errorf("id = %v, want a string", body["id"])
	}
	if body["name"] != "Garage" {
		t.Errorf("name = %v, want %q", body["name"], "Garage")
	}
}

func TestLocationsAPI_Create_IntegrationPrincipalForbidden(t *testing.T) {
	handlers := adapter.NewLocationsAPIHandlers(newFakeLocationService(), testLogger())
	server := newAPIPrincipalServer(t, identity.NewIntegrationPrincipal("Nestova"), handlers.Routes)

	resp := apiPost(t, server, "/api/v1/locations", `{"name":"Garage"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	assertErrorCode(t, resp, "forbidden")
}

func TestLocationsAPI_Create_BlankNameReturns422WithFieldDetail(t *testing.T) {
	handlers := adapter.NewLocationsAPIHandlers(newFakeLocationService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiPost(t, server, "/api/v1/locations", `{"name":"   "}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	assertFieldDetail(t, resp, "name")
}

func TestLocationsAPI_Get_NotFound(t *testing.T) {
	handlers := adapter.NewLocationsAPIHandlers(newFakeLocationService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiGet(t, server, "/api/v1/locations/"+domain.NewLocationID().String())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	assertErrorCode(t, resp, "not_found")
}

func TestLocationsAPI_Get_MalformedIDReturns400(t *testing.T) {
	handlers := adapter.NewLocationsAPIHandlers(newFakeLocationService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiGet(t, server, "/api/v1/locations/not-a-uuid")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestLocationsAPI_Update_RenamesAndReturnsUpdated(t *testing.T) {
	locations := newFakeLocationService()
	l := domain.Location{ID: domain.NewLocationID(), Name: "Old name"}
	locations.locations[l.ID] = l
	handlers := adapter.NewLocationsAPIHandlers(locations, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiPut(t, server, "/api/v1/locations/"+l.ID.String(), `{"name":"New name"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["name"] != "New name" {
		t.Errorf("name = %v, want %q", body["name"], "New name")
	}
}

func TestLocationsAPI_Delete_Success(t *testing.T) {
	locations := newFakeLocationService()
	l := domain.Location{ID: domain.NewLocationID(), Name: "Garage"}
	locations.locations[l.ID] = l
	handlers := adapter.NewLocationsAPIHandlers(locations, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiDelete(t, server, "/api/v1/locations/"+l.ID.String())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func TestLocationsAPI_Delete_NotEmptyReturns409(t *testing.T) {
	locations := newFakeLocationService()
	l := domain.Location{ID: domain.NewLocationID(), Name: "Garage"}
	locations.locations[l.ID] = l
	locations.deleteErr = domain.ErrLocationNotEmpty
	handlers := adapter.NewLocationsAPIHandlers(locations, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiDelete(t, server, "/api/v1/locations/"+l.ID.String())
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
	assertErrorCode(t, resp, "conflict")
}

func TestLocationsAPI_List_Pagination(t *testing.T) {
	locations := newFakeLocationService()
	for _, name := range []string{"Attic", "Basement", "Garage"} {
		l := domain.Location{ID: domain.NewLocationID(), Name: name}
		locations.locations[l.ID] = l
	}
	handlers := adapter.NewLocationsAPIHandlers(locations, testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	first := apiGet(t, server, "/api/v1/locations?limit=2")
	defer func() { _ = first.Body.Close() }()
	var firstBody struct {
		Locations []struct {
			Name string `json:"name"`
		} `json:"locations"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.NewDecoder(first.Body).Decode(&firstBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(firstBody.Locations) != 2 || firstBody.NextCursor == nil {
		t.Fatalf("first page = %+v, want 2 rows and a next_cursor", firstBody)
	}

	second := apiGet(t, server, "/api/v1/locations?limit=2&before="+*firstBody.NextCursor)
	defer func() { _ = second.Body.Close() }()
	var secondBody struct {
		Locations []struct {
			Name string `json:"name"`
		} `json:"locations"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.NewDecoder(second.Body).Decode(&secondBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(secondBody.Locations) != 1 || secondBody.NextCursor != nil {
		t.Errorf("second page = %+v, want exactly 1 row and no next_cursor", secondBody)
	}
}

func TestLocationsAPI_List_MalformedCursorReturns400(t *testing.T) {
	handlers := adapter.NewLocationsAPIHandlers(newFakeLocationService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiGet(t, server, "/api/v1/locations?before=not-a-cursor!!!")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	assertFieldDetail(t, resp, "before")
}

func TestLocationsAPI_Create_MalformedBodyReturns400(t *testing.T) {
	handlers := adapter.NewLocationsAPIHandlers(newFakeLocationService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiPost(t, server, "/api/v1/locations", `{not json`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestLocationsAPI_MethodMismatch_JSON405(t *testing.T) {
	handlers := adapter.NewLocationsAPIHandlers(newFakeLocationService(), testLogger())
	server := newAPIPrincipalServer(t, testViewer(), handlers.Routes)

	resp := apiRequest(t, server, http.MethodPatch, "/api/v1/locations/"+domain.NewLocationID().String(), "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}
