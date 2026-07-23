package adapter_test

import (
	"context"
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

// fakeLocationService is a configurable locationQueryCommandService fake
// for LocationsWebHandlers' hermetic unit tests.
type fakeLocationService struct {
	locations map[domain.LocationID]domain.Location

	listErr   error
	getErr    error
	createErr error
	renameErr error
	deleteErr error

	createCalls int
}

func newFakeLocationService() *fakeLocationService {
	return &fakeLocationService{locations: map[domain.LocationID]domain.Location{}}
}

func (f *fakeLocationService) List(_ context.Context, _ identity.Principal) ([]app.LocationSummary, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	summaries := make([]app.LocationSummary, 0, len(f.locations))
	for _, l := range f.locations {
		summaries = append(summaries, app.LocationSummary{Location: l})
	}
	return summaries, nil
}

func (f *fakeLocationService) Get(_ context.Context, _ identity.Principal, id domain.LocationID) (*domain.Location, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	l, ok := f.locations[id]
	if !ok {
		return nil, domain.ErrLocationNotFound
	}
	return &l, nil
}

func (f *fakeLocationService) Create(_ context.Context, name, _ string, _ *domain.LocationID, createdBy identity.UserID) (*domain.Location, error) {
	f.createCalls++
	if f.createErr != nil {
		return nil, f.createErr
	}
	// Mirrors app.LocationService.Create's own validation (domain.
	// ValidateLocationName) so this fake's observable contract matches the
	// real service's: a blank name is rejected here too, not only by a
	// configured createErr, which is what actually exercises
	// LocationsWebHandlers' mapLocationError path for this case.
	if strings.TrimSpace(name) == "" {
		return nil, domain.ErrInvalidLocationName
	}
	l := domain.Location{ID: domain.NewLocationID(), Name: strings.TrimSpace(name), CreatedBy: createdBy}
	f.locations[l.ID] = l
	return &l, nil
}

func (f *fakeLocationService) Rename(_ context.Context, id domain.LocationID, name string) error {
	if f.renameErr != nil {
		return f.renameErr
	}
	l, ok := f.locations[id]
	if !ok {
		return domain.ErrLocationNotFound
	}
	l.Name = name
	f.locations[id] = l
	return nil
}

func (f *fakeLocationService) Delete(_ context.Context, id domain.LocationID) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.locations[id]; !ok {
		return domain.ErrLocationNotFound
	}
	delete(f.locations, id)
	return nil
}

// fakeBinsByLocation is a configurable binsByLocation fake for
// LocationsWebHandlers' own detail page.
type fakeBinsByLocation struct {
	views []app.BinView
	err   error
}

func (f *fakeBinsByLocation) ListVisibleByLocation(_ context.Context, _ identity.Principal, _ domain.LocationID) ([]app.BinView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.views, nil
}

// locationsWebHarness bundles a running LocationsWebHandlers server and a
// client carrying its session cookie across requests.
type locationsWebHarness struct {
	server    *httptest.Server
	client    *http.Client
	locations *fakeLocationService
}

func newLocationsWebHarness(t *testing.T, viewer identity.Principal, locations *fakeLocationService, bins *fakeBinsByLocation) *locationsWebHarness {
	t.Helper()
	sm := scs.New()
	handlers := adapter.NewLocationsWebHandlers(locations, bins, sm, testLayout, testLogger())
	server := newPrincipalServer(t, sm, viewer, handlers.Routes)
	return &locationsWebHarness{server: server, client: newCSRFClient(t), locations: locations}
}

func (h *locationsWebHarness) getCSRF(t *testing.T, path string) string {
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

func (h *locationsWebHarness) postForm(t *testing.T, path string, form url.Values, htmx bool) (*http.Response, string) {
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

func TestNewLocationsWebHandlers_NilDependenciesPanic(t *testing.T) {
	locations, bins := newFakeLocationService(), &fakeBinsByLocation{}
	sm := scs.New()
	tests := []struct {
		name string
		fn   func()
	}{
		{"nil locations", func() { adapter.NewLocationsWebHandlers(nil, bins, sm, testLayout, testLogger()) }},
		{"nil bins", func() { adapter.NewLocationsWebHandlers(locations, nil, sm, testLayout, testLogger()) }},
		{"nil session manager", func() { adapter.NewLocationsWebHandlers(locations, bins, nil, testLayout, testLogger()) }},
		{"nil layout", func() { adapter.NewLocationsWebHandlers(locations, bins, sm, nil, testLogger()) }},
		{"nil logger", func() { adapter.NewLocationsWebHandlers(locations, bins, sm, testLayout, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewLocationsWebHandlers(%s) did not panic", tt.name)
				}
			}()
			tt.fn()
		})
	}
}

func TestLocationsWebHandlers_List_FullNavigation_WrapsInLayout(t *testing.T) {
	locations := newFakeLocationService()
	loc := domain.Location{ID: domain.NewLocationID(), Name: "Garage"}
	locations.locations[loc.ID] = loc
	h := newLocationsWebHarness(t, testViewer(), locations, &fakeBinsByLocation{})

	resp, err := h.client.Get(h.server.URL + "/locations")
	if err != nil {
		t.Fatalf("GET /locations: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "<layout>") {
		t.Error("full navigation response was not wrapped in the layout")
	}
	if !strings.Contains(string(body), "Garage") {
		t.Error("response missing the seeded location's name")
	}
}

func TestLocationsWebHandlers_Create_CSRFRejected(t *testing.T) {
	locations := newFakeLocationService()
	h := newLocationsWebHarness(t, testViewer(), locations, &fakeBinsByLocation{})
	h.getCSRF(t, "/locations")

	resp, _ := h.postForm(t, "/locations", url.Values{"csrf_token": {"wrong"}, "name": {"Garage"}}, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /locations (bad CSRF) = %d, want 403", resp.StatusCode)
	}
	if locations.createCalls != 0 {
		t.Error("Create must not be called when CSRF verification fails")
	}
}

func TestLocationsWebHandlers_Create_ValidationRejected(t *testing.T) {
	locations := newFakeLocationService()
	h := newLocationsWebHarness(t, testViewer(), locations, &fakeBinsByLocation{})
	csrf := h.getCSRF(t, "/locations")

	resp, body := h.postForm(t, "/locations", url.Values{"csrf_token": {csrf}, "name": {"   "}}, true)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /locations (blank name) = %d, want 422:\n%s", resp.StatusCode, body)
	}
	if len(locations.locations) != 0 {
		t.Error("a blank name must not create a location")
	}
}

func TestLocationsWebHandlers_Create_Success_Finishes(t *testing.T) {
	locations := newFakeLocationService()
	h := newLocationsWebHarness(t, testViewer(), locations, &fakeBinsByLocation{})
	csrf := h.getCSRF(t, "/locations")

	resp, _ := h.postForm(t, "/locations", url.Values{"csrf_token": {csrf}, "name": {"Garage"}}, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("POST /locations (valid) = %d, want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/locations" {
		t.Errorf("Location = %q, want %q", got, "/locations")
	}
	if locations.createCalls != 1 {
		t.Errorf("Create was called %d times, want 1", locations.createCalls)
	}
}

func TestLocationsWebHandlers_Delete_RejectedNotEmpty_RerendersDetail(t *testing.T) {
	locations := newFakeLocationService()
	loc := domain.Location{ID: domain.NewLocationID(), Name: "Garage"}
	locations.locations[loc.ID] = loc
	locations.deleteErr = domain.ErrLocationNotEmpty
	h := newLocationsWebHarness(t, testViewer(), locations, &fakeBinsByLocation{})
	csrf := h.getCSRF(t, "/locations/"+loc.ID.String())

	resp, body := h.postForm(t, "/locations/"+loc.ID.String()+"/delete", url.Values{"csrf_token": {csrf}}, true)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST delete (not empty) = %d, want 409:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Garage") {
		t.Error("rejected delete did not re-render the location detail fragment")
	}
}

func TestLocationsWebHandlers_Delete_Success_Redirects(t *testing.T) {
	locations := newFakeLocationService()
	loc := domain.Location{ID: domain.NewLocationID(), Name: "Garage"}
	locations.locations[loc.ID] = loc
	h := newLocationsWebHarness(t, testViewer(), locations, &fakeBinsByLocation{})
	csrf := h.getCSRF(t, "/locations/"+loc.ID.String())

	resp, _ := h.postForm(t, "/locations/"+loc.ID.String()+"/delete", url.Values{"csrf_token": {csrf}}, false)
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("POST delete (success) = %d, want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/locations" {
		t.Errorf("Location = %q, want %q", got, "/locations")
	}
}
