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

// fakeItemOperationService is a configurable itemOperationService fake for
// OperationsAPIHandlers' hermetic unit tests: each method's own call count
// and next result/error are set directly by the test, mirroring
// fakeItemAPIService's own per-call configurability (items_api_test.go).
type fakeItemOperationService struct {
	addToBinCalls int
	addToBinErr   error
	addToBinOp    app.Operation

	removeFromBinCalls int
	removeFromBinErr   error
	removeFromBinOp    app.Operation

	returnToBinCalls int
	returnToBinErr   error
	returnToBinOp    app.Operation
}

func (f *fakeItemOperationService) AddToBin(_ context.Context, _ identity.Principal, _ domain.ItemID, _ domain.BinID) (app.Operation, error) {
	f.addToBinCalls++
	if f.addToBinErr != nil {
		return app.Operation{}, f.addToBinErr
	}
	return f.addToBinOp, nil
}

func (f *fakeItemOperationService) RemoveFromBin(_ context.Context, _ identity.Principal, _ domain.ItemID, _ *string) (app.Operation, error) {
	f.removeFromBinCalls++
	if f.removeFromBinErr != nil {
		return app.Operation{}, f.removeFromBinErr
	}
	return f.removeFromBinOp, nil
}

func (f *fakeItemOperationService) ReturnToBin(_ context.Context, _ identity.Principal, _ domain.ItemID, _ domain.BinID, _ *string) (app.Operation, error) {
	f.returnToBinCalls++
	if f.returnToBinErr != nil {
		return app.Operation{}, f.returnToBinErr
	}
	return f.returnToBinOp, nil
}

// fakeBinMoveService is a configurable binAPIMover fake for
// OperationsAPIHandlers' hermetic unit tests.
type fakeBinMoveService struct {
	calls  int
	err    error
	result app.MoveResult
}

func (f *fakeBinMoveService) Move(_ context.Context, _ identity.Principal, _ domain.BinID, _ domain.LocationID) (app.MoveResult, error) {
	f.calls++
	if f.err != nil {
		return app.MoveResult{}, f.err
	}
	return f.result, nil
}

// fakeItemDetailReader is a configurable itemDetailReader fake for
// OperationsAPIHandlers/HistoryAPIHandlers' hermetic unit tests.
type fakeItemDetailReader struct {
	calls  int
	result *domain.ItemDetailResult
	err    error
}

func (f *fakeItemDetailReader) Detail(_ context.Context, _ identity.Principal, _ domain.ItemID) (*domain.ItemDetailResult, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

// fakeReturnRequestAPIOperator is a configurable returnRequestOperator fake
// for OperationsAPIHandlers' hermetic unit tests: unlike
// fakeReturnRequestOperator (items_web_test.go), which drives an in-memory
// slice for ItemsWebHandlers' broader detail-page tests, each method's
// error and ListForItem's own result are set directly here, since this
// file's tests simulate specific idempotent-retry conditions (an existing
// open request, an already-cancelled one, a fulfilled one) that fake does
// not model.
type fakeReturnRequestAPIOperator struct {
	requestErr   error
	requestOp    *domain.ReturnRequest
	requestCalls int

	cancelErr   error
	cancelCalls int

	listResult []domain.ReturnRequest
	listErr    error
	listCalls  int
}

func (f *fakeReturnRequestAPIOperator) Request(_ context.Context, _ identity.Principal, _ domain.ItemID, _ *string) (*domain.ReturnRequest, error) {
	f.requestCalls++
	if f.requestErr != nil {
		return nil, f.requestErr
	}
	return f.requestOp, nil
}

func (f *fakeReturnRequestAPIOperator) Cancel(_ context.Context, _ identity.Principal, _ domain.ItemID, _ domain.ReturnRequestID) error {
	f.cancelCalls++
	return f.cancelErr
}

func (f *fakeReturnRequestAPIOperator) ListForItem(_ context.Context, _ identity.Principal, _ domain.ItemID) ([]domain.ReturnRequest, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listResult, nil
}

// operationsAPIFakes bundles OperationsAPIHandlers' four dependencies so
// each test only names the ones it configures.
type operationsAPIFakes struct {
	ops    *fakeItemOperationService
	mover  *fakeBinMoveService
	rr     *fakeReturnRequestAPIOperator
	detail *fakeItemDetailReader
}

func newOperationsAPIFakes() *operationsAPIFakes {
	return &operationsAPIFakes{
		ops: &fakeItemOperationService{}, mover: &fakeBinMoveService{},
		rr: &fakeReturnRequestAPIOperator{}, detail: &fakeItemDetailReader{},
	}
}

func (f *operationsAPIFakes) handlers() *adapter.OperationsAPIHandlers {
	return adapter.NewOperationsAPIHandlers(f.ops, f.mover, f.rr, f.detail, testLogger())
}

func TestNewOperationsAPIHandlers_NilDependenciesPanic(t *testing.T) {
	cases := map[string]func(f *operationsAPIFakes) func(){
		"nil operations": func(f *operationsAPIFakes) func() {
			return func() { adapter.NewOperationsAPIHandlers(nil, f.mover, f.rr, f.detail, testLogger()) }
		},
		"nil mover": func(f *operationsAPIFakes) func() {
			return func() { adapter.NewOperationsAPIHandlers(f.ops, nil, f.rr, f.detail, testLogger()) }
		},
		"nil return requests": func(f *operationsAPIFakes) func() {
			return func() { adapter.NewOperationsAPIHandlers(f.ops, f.mover, nil, f.detail, testLogger()) }
		},
		"nil detail": func(f *operationsAPIFakes) func() {
			return func() { adapter.NewOperationsAPIHandlers(f.ops, f.mover, f.rr, nil, testLogger()) }
		},
		"nil logger": func(f *operationsAPIFakes) func() {
			return func() { adapter.NewOperationsAPIHandlers(f.ops, f.mover, f.rr, f.detail, nil) }
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: did not panic", name)
				}
			}()
			build(newOperationsAPIFakes())()
		})
	}
}

func testItem(id domain.ItemID, binID *domain.BinID, heldBy *identity.UserID) domain.Item {
	now := time.Now()
	return domain.Item{
		ID: id, Name: "Stove", Quantity: 1, CurrentBinID: binID, HeldBy: heldBy,
		PlacementChangedAt: now, CreatedAt: now, UpdatedAt: now,
	}
}

// --- AddToBin ---

func TestOperationsAPI_AddToBin_Success(t *testing.T) {
	f := newOperationsAPIFakes()
	itemID, binID := domain.NewItemID(), domain.NewBinID()
	it := testItem(itemID, &binID, nil)
	f.ops.addToBinOp = app.Operation{Verb: app.OperationAdd, Item: &it, BinID: &binID}
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+itemID.String()+"/add-to-bin", `{"bin_id":"`+binID.String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["bin_id"] != binID.String() || body["state"] != "in_bin" {
		t.Errorf("body = %+v, want bin_id/state reflecting the request", body)
	}
	if f.ops.addToBinCalls != 1 {
		t.Errorf("AddToBin calls = %d, want 1", f.ops.addToBinCalls)
	}
}

func TestOperationsAPI_AddToBin_RetriedIntoSameBin_Returns200NoSecondCall(t *testing.T) {
	f := newOperationsAPIFakes()
	itemID, binID := domain.NewItemID(), domain.NewBinID()
	f.ops.addToBinErr = domain.ErrItemAlreadyInBin
	f.detail.result = &domain.ItemDetailResult{Item: testItem(itemID, &binID, nil)}
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+itemID.String()+"/add-to-bin", `{"bin_id":"`+binID.String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d (idempotent retry)", resp.StatusCode, http.StatusOK)
	}
	if f.ops.addToBinCalls != 1 {
		t.Errorf("AddToBin calls = %d, want 1 (no duplicate event)", f.ops.addToBinCalls)
	}
	if f.detail.calls != 1 {
		t.Errorf("Detail calls = %d, want 1", f.detail.calls)
	}
}

func TestOperationsAPI_AddToBin_AlreadyInDifferentBin_Returns409(t *testing.T) {
	f := newOperationsAPIFakes()
	itemID, requestedBin, currentBin := domain.NewItemID(), domain.NewBinID(), domain.NewBinID()
	f.ops.addToBinErr = domain.ErrItemAlreadyInBin
	f.detail.result = &domain.ItemDetailResult{Item: testItem(itemID, &currentBin, nil)}
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+itemID.String()+"/add-to-bin", `{"bin_id":"`+requestedBin.String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
	assertErrorCode(t, resp, "item_already_in_bin")
}

func TestOperationsAPI_AddToBin_UnknownBin_Returns404(t *testing.T) {
	f := newOperationsAPIFakes()
	f.ops.addToBinErr = domain.ErrBinNotFound
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+domain.NewItemID().String()+"/add-to-bin", `{"bin_id":"`+domain.NewBinID().String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	assertErrorCode(t, resp, "bin_not_found")
}

func TestOperationsAPI_AddToBin_MalformedBinID_Returns400(t *testing.T) {
	f := newOperationsAPIFakes()
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+domain.NewItemID().String()+"/add-to-bin", `{"bin_id":"not-a-uuid"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	assertFieldDetail(t, resp, "bin_id")
}

// --- CheckOut ---

func TestOperationsAPI_CheckOut_Success(t *testing.T) {
	f := newOperationsAPIFakes()
	itemID := domain.NewItemID()
	viewer := testViewer()
	it := testItem(itemID, nil, &viewer.UserID)
	f.ops.removeFromBinOp = app.Operation{Verb: app.OperationRemove, Item: &it}
	server := newAPIPrincipalServer(t, viewer, f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+itemID.String()+"/check-out", `{}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["state"] != "checked_out" {
		t.Errorf("body = %+v, want state = checked_out", body)
	}
}

func TestOperationsAPI_CheckOut_RetriedBySameHolder_Returns200(t *testing.T) {
	f := newOperationsAPIFakes()
	itemID := domain.NewItemID()
	viewer := testViewer()
	f.ops.removeFromBinErr = domain.ErrItemAlreadyCheckedOut
	f.detail.result = &domain.ItemDetailResult{Item: testItem(itemID, nil, &viewer.UserID)}
	server := newAPIPrincipalServer(t, viewer, f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+itemID.String()+"/check-out", `{}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d (idempotent retry)", resp.StatusCode, http.StatusOK)
	}
	if f.ops.removeFromBinCalls != 1 {
		t.Errorf("RemoveFromBin calls = %d, want 1 (no duplicate event)", f.ops.removeFromBinCalls)
	}
}

func TestOperationsAPI_CheckOut_HeldByDifferentUser_Returns409(t *testing.T) {
	f := newOperationsAPIFakes()
	itemID := domain.NewItemID()
	otherHolder := identity.NewUserID()
	f.ops.removeFromBinErr = domain.ErrItemAlreadyCheckedOut
	f.detail.result = &domain.ItemDetailResult{Item: testItem(itemID, nil, &otherHolder)}
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+itemID.String()+"/check-out", `{}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
	assertErrorCode(t, resp, "item_already_checked_out")
}

func TestOperationsAPI_CheckOut_IntegrationPrincipal_Returns403(t *testing.T) {
	f := newOperationsAPIFakes()
	f.ops.removeFromBinErr = domain.ErrHolderRequired
	server := newAPIPrincipalServer(t, identity.NewIntegrationPrincipal("Nestova"), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+domain.NewItemID().String()+"/check-out", `{}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	assertErrorCode(t, resp, "holder_required")
}

// --- Return ---

func TestOperationsAPI_Return_Success(t *testing.T) {
	f := newOperationsAPIFakes()
	itemID, binID := domain.NewItemID(), domain.NewBinID()
	it := testItem(itemID, &binID, nil)
	f.ops.returnToBinOp = app.Operation{Verb: app.OperationReturn, Item: &it, BinID: &binID}
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+itemID.String()+"/return", `{"bin_id":"`+binID.String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestOperationsAPI_Return_RetriedIntoSameBin_Returns200(t *testing.T) {
	f := newOperationsAPIFakes()
	itemID, binID := domain.NewItemID(), domain.NewBinID()
	f.ops.returnToBinErr = domain.ErrItemNotCheckedOut
	f.detail.result = &domain.ItemDetailResult{Item: testItem(itemID, &binID, nil)}
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+itemID.String()+"/return", `{"bin_id":"`+binID.String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d (idempotent retry)", resp.StatusCode, http.StatusOK)
	}
	if f.ops.returnToBinCalls != 1 {
		t.Errorf("ReturnToBin calls = %d, want 1 (no duplicate event)", f.ops.returnToBinCalls)
	}
}

func TestOperationsAPI_Return_ElsewhereNotCheckedOut_Returns409(t *testing.T) {
	f := newOperationsAPIFakes()
	itemID, requestedBin, currentBin := domain.NewItemID(), domain.NewBinID(), domain.NewBinID()
	f.ops.returnToBinErr = domain.ErrItemNotCheckedOut
	f.detail.result = &domain.ItemDetailResult{Item: testItem(itemID, &currentBin, nil)}
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+itemID.String()+"/return", `{"bin_id":"`+requestedBin.String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
	assertErrorCode(t, resp, "item_not_checked_out")
}

// --- MoveBin ---

func TestOperationsAPI_MoveBin_Success(t *testing.T) {
	f := newOperationsAPIFakes()
	binID, from, to := domain.NewBinID(), domain.NewLocationID(), domain.NewLocationID()
	movedAt := time.Now()
	f.mover.result = app.MoveResult{BinID: binID, FromLocationID: from, ToLocationID: to, MovedAt: movedAt}
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/bins/"+binID.String()+"/move", `{"location_id":"`+to.String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var body struct {
		BinID          string  `json:"bin_id"`
		FromLocationID string  `json:"from_location_id"`
		ToLocationID   string  `json:"to_location_id"`
		MovedAt        *string `json:"moved_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.FromLocationID != from.String() || body.ToLocationID != to.String() || body.MovedAt == nil {
		t.Errorf("body = %+v, want from/to reflecting the move and a non-null moved_at", body)
	}
}

func TestOperationsAPI_MoveBin_RetriedToSameLocation_Returns200WithNilMovedAt(t *testing.T) {
	f := newOperationsAPIFakes()
	binID, target := domain.NewBinID(), domain.NewLocationID()
	f.mover.err = domain.ErrBinAlreadyInLocation
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/bins/"+binID.String()+"/move", `{"location_id":"`+target.String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d (idempotent retry)", resp.StatusCode, http.StatusOK)
	}
	var body struct {
		FromLocationID string  `json:"from_location_id"`
		ToLocationID   string  `json:"to_location_id"`
		MovedAt        *string `json:"moved_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.FromLocationID != target.String() || body.ToLocationID != target.String() || body.MovedAt != nil {
		t.Errorf("body = %+v, want from=to=%v and a null moved_at", body, target)
	}
}

func TestOperationsAPI_MoveBin_UnknownLocation_Returns404(t *testing.T) {
	f := newOperationsAPIFakes()
	f.mover.err = domain.ErrLocationNotFound
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/bins/"+domain.NewBinID().String()+"/move", `{"location_id":"`+domain.NewLocationID().String()+`"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	assertErrorCode(t, resp, "location_not_found")
}

// --- RequestReturn ---

func TestOperationsAPI_RequestReturn_Success(t *testing.T) {
	f := newOperationsAPIFakes()
	itemID := domain.NewItemID()
	created := &domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: itemID, Status: domain.ReturnRequestStatusOpen, CreatedAt: time.Now()}
	f.rr.requestOp = created
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+itemID.String()+"/return-requests", `{}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["id"] != created.ID.String() || body["status"] != "open" {
		t.Errorf("body = %+v, want the created request", body)
	}
}

func TestOperationsAPI_RequestReturn_RetriedByRequester_Returns200ExistingRequest(t *testing.T) {
	f := newOperationsAPIFakes()
	itemID := domain.NewItemID()
	viewer := testViewer()
	existing := domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: itemID, RequesterID: viewer.UserID, Status: domain.ReturnRequestStatusOpen}
	f.rr.requestErr = domain.ErrDuplicateReturnRequest
	f.rr.listResult = []domain.ReturnRequest{existing}
	server := newAPIPrincipalServer(t, viewer, f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+itemID.String()+"/return-requests", `{}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d (idempotent retry)", resp.StatusCode, http.StatusOK)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["id"] != existing.ID.String() {
		t.Errorf("body = %+v, want the requester's own existing open request %v", body, existing.ID)
	}
	if f.rr.requestCalls != 1 {
		t.Errorf("Request calls = %d, want 1 (no duplicate event)", f.rr.requestCalls)
	}
}

func TestOperationsAPI_RequestReturn_InvalidMessage_Returns422WithFieldDetail(t *testing.T) {
	f := newOperationsAPIFakes()
	f.rr.requestErr = domain.ErrInvalidReturnRequestMessage
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+domain.NewItemID().String()+"/return-requests", `{"message":"  "}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	assertFieldDetail(t, resp, "message")
}

// --- CancelReturnRequest ---

func TestOperationsAPI_CancelReturnRequest_Success(t *testing.T) {
	f := newOperationsAPIFakes()
	itemID, requestID := domain.NewItemID(), domain.NewReturnRequestID()
	f.rr.listResult = []domain.ReturnRequest{{ID: requestID, ItemID: itemID, Status: domain.ReturnRequestStatusCancelled}}
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+itemID.String()+"/return-requests/"+requestID.String()+"/cancel", "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "cancelled" {
		t.Errorf("body = %+v, want status = cancelled", body)
	}
	if f.rr.cancelCalls != 1 {
		t.Errorf("Cancel calls = %d, want 1", f.rr.cancelCalls)
	}
}

func TestOperationsAPI_CancelReturnRequest_RetriedAlreadyCancelled_Returns200(t *testing.T) {
	f := newOperationsAPIFakes()
	itemID, requestID := domain.NewItemID(), domain.NewReturnRequestID()
	f.rr.cancelErr = domain.ErrReturnRequestNotOpen
	f.rr.listResult = []domain.ReturnRequest{{ID: requestID, ItemID: itemID, Status: domain.ReturnRequestStatusCancelled}}
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+itemID.String()+"/return-requests/"+requestID.String()+"/cancel", "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d (idempotent retry)", resp.StatusCode, http.StatusOK)
	}
	if f.rr.cancelCalls != 1 {
		t.Errorf("Cancel calls = %d, want 1 (no duplicate event)", f.rr.cancelCalls)
	}
}

func TestOperationsAPI_CancelReturnRequest_AlreadyFulfilled_Returns409(t *testing.T) {
	f := newOperationsAPIFakes()
	itemID, requestID := domain.NewItemID(), domain.NewReturnRequestID()
	f.rr.cancelErr = domain.ErrReturnRequestNotOpen
	f.rr.listResult = []domain.ReturnRequest{{ID: requestID, ItemID: itemID, Status: domain.ReturnRequestStatusFulfilled}}
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+itemID.String()+"/return-requests/"+requestID.String()+"/cancel", "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
	assertErrorCode(t, resp, "return_request_not_open")
}

func TestOperationsAPI_CancelReturnRequest_Unknown_Returns404(t *testing.T) {
	f := newOperationsAPIFakes()
	f.rr.cancelErr = domain.ErrReturnRequestNotFound
	server := newAPIPrincipalServer(t, testViewer(), f.handlers().Routes)

	resp := apiPost(t, server, "/api/v1/items/"+domain.NewItemID().String()+"/return-requests/"+domain.NewReturnRequestID().String()+"/cancel", "")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	assertErrorCode(t, resp, "return_request_not_found")
}
