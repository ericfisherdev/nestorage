package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pb33f/libopenapi"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	mediaadapter "github.com/ericfisherdev/nestorage/internal/media/adapter"
	mediadomain "github.com/ericfisherdev/nestorage/internal/media/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/api"
	storageadapter "github.com/ericfisherdev/nestorage/internal/storage/adapter"
	storageapp "github.com/ericfisherdev/nestorage/internal/storage/app"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// recordingRegistrar is an api.Registrar test double: it wraps a real
// *http.ServeMux, so a genuinely conflicting registration still panics
// exactly as production's api.Router would, and additionally records every
// "METHOD /pattern" string passed to Handle/HandleFunc — the one thing
// *http.ServeMux itself has no way to report back (Go 1.26's
// http.ServeMux exposes no route-enumeration API), which is the reason
// api.Registrar exists at all (see its own doc, internal/platform/api/router.go).
type recordingRegistrar struct {
	mux      *http.ServeMux
	patterns map[string]bool
}

func newRecordingRegistrar() *recordingRegistrar {
	return &recordingRegistrar{mux: http.NewServeMux(), patterns: make(map[string]bool)}
}

func (r *recordingRegistrar) Handle(pattern string, handler http.Handler) {
	r.mux.Handle(pattern, handler)
	r.patterns[pattern] = true
}

func (r *recordingRegistrar) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	r.mux.HandleFunc(pattern, handler)
	r.patterns[pattern] = true
}

// Every stub type below satisfies exactly one bounded-context handler
// group's own narrow port (ISP), constructed purely so
// TestAPIRoutes_MatchOpenAPISpec below can build the real *XxxAPIHandlers
// types Routes is defined on. None of their methods is ever called — this
// test only exercises route REGISTRATION, never a handler's own request
// handling (that is each package's own hermetic unit tests' job) — so every
// method here is unreachable and returns a zero value.

type stubLocationService struct{}

func (stubLocationService) List(context.Context, identity.Principal) ([]storageapp.LocationSummary, error) {
	return nil, nil
}

func (stubLocationService) Get(context.Context, identity.Principal, storagedomain.LocationID) (*storagedomain.Location, error) {
	return nil, nil
}

func (stubLocationService) Create(context.Context, string, string, *storagedomain.LocationID, identity.Principal) (*storagedomain.Location, error) {
	return nil, nil
}

func (stubLocationService) Rename(context.Context, identity.Principal, storagedomain.LocationID, string) error {
	return nil
}

func (stubLocationService) Delete(context.Context, identity.Principal, storagedomain.LocationID) error {
	return nil
}

// stubBinService satisfies both binAPIService (BinsAPIHandlers) and
// binDetailReader (HistoryAPIHandlers) — binDetailReader's own GetByID has
// the identical signature this type already carries for binAPIService, so
// one stub covers both handler groups' own dependency.
type stubBinService struct{}

func (stubBinService) ListVisible(context.Context, identity.Principal) ([]storageapp.BinView, error) {
	return nil, nil
}

func (stubBinService) ListVisibleByLocation(context.Context, identity.Principal, storagedomain.LocationID) ([]storageapp.BinView, error) {
	return nil, nil
}

func (stubBinService) GetByID(context.Context, identity.Principal, storagedomain.BinID) (*storageapp.BinView, error) {
	return nil, nil
}

func (stubBinService) Create(context.Context, storageapp.CreateBinInput) (*storagedomain.Bin, error) {
	return nil, nil
}

func (stubBinService) Edit(context.Context, identity.Principal, storagedomain.BinID, string, string, *identity.UserID, storagedomain.Visibility) error {
	return nil
}

func (stubBinService) Delete(context.Context, identity.Principal, storagedomain.BinID) error {
	return nil
}

type stubItemService struct{}

func (stubItemService) Get(context.Context, identity.Principal, storagedomain.ItemID) (*storagedomain.Item, error) {
	return nil, nil
}

func (stubItemService) ListVisible(context.Context, identity.Principal, storagedomain.ItemFilter) ([]storagedomain.Item, error) {
	return nil, nil
}

func (stubItemService) Create(context.Context, string, *string, int, storagedomain.BinID, identity.Principal) (*storagedomain.Item, error) {
	return nil, nil
}

func (stubItemService) Edit(context.Context, storagedomain.ItemID, string, *string, int, identity.Principal) error {
	return nil
}

func (stubItemService) Delete(context.Context, storagedomain.ItemID, identity.Principal) error {
	return nil
}

type stubOperationService struct{}

func (stubOperationService) AddToBin(context.Context, identity.Principal, storagedomain.ItemID, storagedomain.BinID) (storageapp.Operation, error) {
	return storageapp.Operation{}, nil
}

func (stubOperationService) RemoveFromBin(context.Context, identity.Principal, storagedomain.ItemID, *string) (storageapp.Operation, error) {
	return storageapp.Operation{}, nil
}

func (stubOperationService) ReturnToBin(context.Context, identity.Principal, storagedomain.ItemID, storagedomain.BinID, *string) (storageapp.Operation, error) {
	return storageapp.Operation{}, nil
}

type stubBinMover struct{}

func (stubBinMover) Move(context.Context, identity.Principal, storagedomain.BinID, storagedomain.LocationID) (storageapp.MoveResult, error) {
	return storageapp.MoveResult{}, nil
}

type stubReturnRequestService struct{}

func (stubReturnRequestService) Request(context.Context, identity.Principal, storagedomain.ItemID, *string) (*storagedomain.ReturnRequest, error) {
	return nil, nil
}

func (stubReturnRequestService) Cancel(context.Context, identity.Principal, storagedomain.ItemID, storagedomain.ReturnRequestID) error {
	return nil
}

func (stubReturnRequestService) ListForItem(context.Context, identity.Principal, storagedomain.ItemID) ([]storagedomain.ReturnRequest, error) {
	return nil, nil
}

// stubItemDetailReader satisfies itemDetailReader, shared by
// OperationsAPIHandlers and HistoryAPIHandlers.
type stubItemDetailReader struct{}

func (stubItemDetailReader) Detail(context.Context, identity.Principal, storagedomain.ItemID) (*storagedomain.ItemDetailResult, error) {
	return nil, nil
}

type stubItemEventLister struct{}

func (stubItemEventLister) ListByItem(context.Context, identity.HouseholdID, storagedomain.ItemID, storagedomain.HistoryPage) ([]storagedomain.ItemEvent, error) {
	return nil, nil
}

type stubBinActivityLister struct{}

func (stubBinActivityLister) ListByBin(context.Context, identity.HouseholdID, storagedomain.BinID, storagedomain.HistoryPage) ([]storagedomain.ItemEvent, error) {
	return nil, nil
}

type stubPhotoOperator struct{}

func (stubPhotoOperator) Upload(context.Context, identity.Principal, storagedomain.ItemID, io.Reader) (*mediadomain.Photo, error) {
	return nil, nil
}

func (stubPhotoOperator) ListForItem(context.Context, identity.Principal, storagedomain.ItemID) ([]mediadomain.ItemPhoto, error) {
	return nil, nil
}

func (stubPhotoOperator) Open(context.Context, identity.Principal, storagedomain.ItemID, mediadomain.PhotoID, mediadomain.PhotoVariant) (io.ReadCloser, string, error) {
	return nil, "", nil
}

func (stubPhotoOperator) DirectURL(context.Context, identity.Principal, storagedomain.ItemID, mediadomain.PhotoID, mediadomain.PhotoVariant) (string, error) {
	return "", nil
}
func (stubPhotoOperator) SupportsDirectURL() bool { return false }
func (stubPhotoOperator) Delete(context.Context, identity.Principal, storagedomain.ItemID, mediadomain.PhotoID) error {
	return nil
}

func (stubPhotoOperator) SetPrimary(context.Context, identity.Principal, storagedomain.ItemID, mediadomain.PhotoID) error {
	return nil
}

func (stubPhotoOperator) Reorder(context.Context, identity.Principal, storagedomain.ItemID, []mediadomain.PhotoID) error {
	return nil
}

type stubDeviceTokenIssuer struct{}

func (stubDeviceTokenIssuer) Issue(context.Context, string, string, string) (string, *identity.DeviceToken, error) {
	return "", nil, nil
}

// testAPIRouteDeps builds the appRouteDeps fields registerAPIRoutes reads,
// plus DeviceTokenAPI and SpecAPI (registered separately, matching
// newAppRoutes' own three call sites) — every field newAppRoutes' OTHER
// route groups need (Onboarding, Login, Bins web, ...) is left zero, since
// this test never calls newAppRoutes itself, only the /api/v1 pieces.
func testAPIRouteDeps(logger *slog.Logger) appRouteDeps {
	return appRouteDeps{
		Logger:         logger,
		DeviceTokenAPI: identityadapter.NewDeviceTokenAPIHandlers(stubDeviceTokenIssuer{}, logger),
		SpecAPI:        api.NewSpecHandlers(logger),
		LocationsAPI:   storageadapter.NewLocationsAPIHandlers(stubLocationService{}, logger),
		BinsAPI:        storageadapter.NewBinsAPIHandlers(stubBinService{}, logger),
		ItemsAPI:       storageadapter.NewItemsAPIHandlers(stubItemService{}, logger),
		OperationsAPI: storageadapter.NewOperationsAPIHandlers(
			stubOperationService{}, stubBinMover{}, stubReturnRequestService{}, stubItemDetailReader{}, logger,
		),
		HistoryAPI: storageadapter.NewHistoryAPIHandlers(
			stubItemDetailReader{}, stubItemEventLister{}, stubBinService{}, stubBinActivityLister{}, logger,
		),
		PhotosAPI: mediaadapter.NewPhotosAPIHandlers(stubPhotoOperator{}, 1<<20, logger),
	}
}

// registeredAPIRoutes builds the real production route set — every /api/v1
// route this binary serves — through the exact same call sequence
// newAppRoutes uses: registerAPIRoutes(deps) for the six bearer-gated CRUD/
// operation/history/photo groups, plus DeviceTokenAPI and SpecAPI's own
// direct registrations (see appRouteDeps.SpecAPI's own doc for why those two
// sit outside registerAPIRoutes). The RequireAPICredential/Observe
// middleware newAPIRouteMount wraps the gated group in is irrelevant to
// route enumeration, so this deliberately calls registerAPIRoutes directly
// rather than going through newAPIRouteMount — shell_test.go's own
// TestAPIMount_* tests already cover that middleware's behavior.
func registeredAPIRoutes(t *testing.T) map[string]bool {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	deps := testAPIRouteDeps(logger)

	reg := newRecordingRegistrar()
	registerAPIRoutes(deps)(reg)
	deps.DeviceTokenAPI.Routes(reg)
	deps.SpecAPI.Routes(reg)
	return reg.patterns
}

// specRoutes parses the OpenAPI document b (the exact bytes GET
// /api/v1/openapi.yaml serves — see TestAPIRoutes_MatchOpenAPISpec) into a
// "METHOD /path" set, one entry per operation. Method keys come back
// lowercase (get/post/...) from libopenapi's own PathItem.GetOperations;
// net/http.ServeMux patterns are upper case, so this upper-cases them to
// compare as plain strings against registeredAPIRoutes' own recorded
// patterns — {id}-style path templates already use identical syntax in
// both worlds (verified against the pb33f/libopenapi v0.38 model), so no
// further translation is needed.
func specRoutes(t *testing.T, b []byte) map[string]bool {
	t.Helper()
	doc, err := libopenapi.NewDocument(b)
	if err != nil {
		t.Fatalf("libopenapi.NewDocument: %v", err)
	}
	model, err := doc.BuildV3Model()
	if err != nil {
		t.Fatalf("BuildV3Model: %v", err)
	}

	routes := make(map[string]bool)
	for path, item := range model.Model.Paths.PathItems.FromOldest() {
		for method, op := range item.GetOperations().FromOldest() {
			if op == nil {
				continue
			}
			routes[strings.ToUpper(method)+" "+path] = true
		}
	}
	return routes
}

// TestAPIRoutes_MatchOpenAPISpec is NSTR-57's own route/spec sync gate: an
// endpoint registered in code with no matching spec entry, or a spec entry
// naming a route nothing registers, fails here — and therefore fails
// `make test` and CI — rather than the two silently drifting apart over
// later tickets.
func TestAPIRoutes_MatchOpenAPISpec(t *testing.T) {
	code := registeredAPIRoutes(t)

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	rec := httptest.NewRecorder()
	api.NewSpecHandlers(logger).Serve(rec, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", nil))
	spec := specRoutes(t, rec.Body.Bytes())

	for route := range code {
		if !spec[route] {
			t.Errorf("registered route %q has no matching openapi.yaml entry", route)
		}
	}
	for route := range spec {
		if !code[route] {
			t.Errorf("openapi.yaml entry %q has no matching registered route", route)
		}
	}
}
