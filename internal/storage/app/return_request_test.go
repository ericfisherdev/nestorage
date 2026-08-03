package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// fakeReturnRequestRepo is an in-memory app.ReturnRequestLifecycleStore +
// app.returnRequestLister fake standing in for the tx-bound/pool-bound
// ReturnRequestRepository a real transactor/composition root constructs.
// Create enforces the same duplicate-open guard the real
// return_request_open_uniq partial index does, and Cancel enforces
// ownership + open-state exactly like the real adapter's own WHERE clause
// (see domain.ReturnRequestRepository.Cancel's own doc).
type fakeReturnRequestRepo struct {
	requests map[domain.ReturnRequestID]*domain.ReturnRequest

	createErr error
	cancelErr error
	listErr   error
}

func newFakeReturnRequestRepo() *fakeReturnRequestRepo {
	return &fakeReturnRequestRepo{requests: make(map[domain.ReturnRequestID]*domain.ReturnRequest)}
}

func (f *fakeReturnRequestRepo) Create(_ context.Context, r *domain.ReturnRequest) error {
	if f.createErr != nil {
		return f.createErr
	}
	for _, existing := range f.requests {
		if existing.ItemID == r.ItemID && existing.RequesterID == r.RequesterID && existing.Status == domain.ReturnRequestStatusOpen {
			return domain.ErrDuplicateReturnRequest
		}
	}
	cp := *r
	cp.CreatedAt = time.Now()
	f.requests[r.ID] = &cp
	*r = cp
	return nil
}

func (f *fakeReturnRequestRepo) Cancel(_ context.Context, _ identity.HouseholdID, id domain.ReturnRequestID, requesterID identity.UserID) (*domain.ReturnRequest, error) {
	if f.cancelErr != nil {
		return nil, f.cancelErr
	}
	existing, ok := f.requests[id]
	if !ok || existing.RequesterID != requesterID {
		return nil, domain.ErrReturnRequestNotFound
	}
	if err := existing.Cancel(time.Now()); err != nil {
		return nil, err
	}
	cp := *existing
	return &cp, nil
}

func (f *fakeReturnRequestRepo) ListByItem(_ context.Context, _ identity.HouseholdID, itemID domain.ItemID) ([]domain.ReturnRequest, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	reqs := make([]domain.ReturnRequest, 0)
	for _, r := range f.requests {
		if r.ItemID == itemID {
			reqs = append(reqs, *r)
		}
	}
	return reqs, nil
}

// fakeReturnRequestItemGetter is a configurable returnRequestItemGetter
// fake: Get returns a copy of item, or getErr when set — mirroring
// fakeItemLinkItemGetter's own rationale, but configurable to a specific
// item (rather than always an empty one) since ReturnRequestService's own
// guards branch on the item's State()/HeldBy.
type fakeReturnRequestItemGetter struct {
	item   *domain.Item
	getErr error
}

func (f *fakeReturnRequestItemGetter) Get(_ context.Context, _ identity.Principal, _ domain.ItemID) (*domain.Item, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	cp := *f.item
	return &cp, nil
}

// fakeReturnRequestUOW is an in-memory returnRequestTransactor fake,
// mirroring fakeUnitOfWork's own rollback simulation: on fn's error, both
// the requests map and the events slice are restored to their pre-call
// snapshot, so a test can assert a rejected Request/Cancel left no partial
// write.
type fakeReturnRequestUOW struct {
	requests *fakeReturnRequestRepo
	events   *fakeEventAppender
}

func (u *fakeReturnRequestUOW) WithinReturnRequestTx(_ context.Context, fn func(app.ReturnRequestTxStores) error) error {
	beforeEvents := append([]domain.ItemEvent(nil), u.events.events...)
	beforeRequests := make(map[domain.ReturnRequestID]*domain.ReturnRequest, len(u.requests.requests))
	for id, r := range u.requests.requests {
		cp := *r
		beforeRequests[id] = &cp
	}

	err := fn(app.ReturnRequestTxStores{ReturnRequests: u.requests, Events: u.events})
	if err != nil {
		u.events.events = beforeEvents
		u.requests.requests = beforeRequests
	}
	return err
}

// newTestReturnRequestService wires a ReturnRequestService over a fresh
// fakeReturnRequestRepo/fakeEventAppender/fakeReturnRequestNotifier,
// returning all three alongside the service so a test can inspect them
// directly.
func newTestReturnRequestService(itemGetter *fakeReturnRequestItemGetter) (*app.ReturnRequestService, *fakeReturnRequestRepo, *fakeEventAppender, *fakeReturnRequestNotifier) {
	requests := newFakeReturnRequestRepo()
	events := &fakeEventAppender{}
	uow := &fakeReturnRequestUOW{requests: requests, events: events}
	notifier := &fakeReturnRequestNotifier{}
	svc := app.NewReturnRequestService(itemGetter, requests, uow, notifier, testLogger())
	return svc, requests, events, notifier
}

// checkedOutItem returns an Item held by holder — the starting state
// Request must accept and ListForItem/Cancel operate against.
func checkedOutItem(holder identity.UserID) *domain.Item {
	return &domain.Item{ID: domain.NewItemID(), Name: "Sleeping bag", Quantity: 1, HeldBy: &holder}
}

// binnedItemFixture returns an Item sitting in a bin — the state Request
// must reject with domain.ErrItemNotCheckedOut.
func binnedItemFixture() *domain.Item {
	bin := domain.NewBinID()
	return &domain.Item{ID: domain.NewItemID(), Name: "Stove", Quantity: 1, CurrentBinID: &bin}
}

func TestNewReturnRequestService_PanicsOnNilDeps(t *testing.T) {
	items := &fakeReturnRequestItemGetter{item: checkedOutItem(identity.NewUserID())}
	requests := newFakeReturnRequestRepo()
	uow := &fakeReturnRequestUOW{requests: requests, events: &fakeEventAppender{}}
	notifier := &fakeReturnRequestNotifier{}

	tests := []struct {
		name  string
		build func()
	}{
		{"nil item getter", func() { app.NewReturnRequestService(nil, requests, uow, notifier, testLogger()) }},
		{"nil lister", func() { app.NewReturnRequestService(items, nil, uow, notifier, testLogger()) }},
		{"nil transactor", func() { app.NewReturnRequestService(items, requests, nil, notifier, testLogger()) }},
		{"nil notifier", func() { app.NewReturnRequestService(items, requests, uow, nil, testLogger()) }},
		{"nil logger", func() { app.NewReturnRequestService(items, requests, uow, notifier, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("NewReturnRequestService did not panic")
				}
			}()
			tt.build()
		})
	}
}

func TestReturnRequestService_Request_Success(t *testing.T) {
	holder := identity.NewUserID()
	itemGetter := &fakeReturnRequestItemGetter{item: checkedOutItem(holder)}
	svc, requests, events, notifier := newTestReturnRequestService(itemGetter)
	requester := identity.NewUserID()
	actor := identity.NewUserPrincipal(requester, identity.RoleAdult, "Riley")
	message := "please, I need this back"

	req, err := svc.Request(context.Background(), actor, itemGetter.item.ID, &message)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.Status != domain.ReturnRequestStatusOpen {
		t.Errorf("Status = %v, want open", req.Status)
	}
	if req.RequesterID != requester || req.HolderID != holder {
		t.Errorf("RequesterID/HolderID = %v/%v, want %v/%v", req.RequesterID, req.HolderID, requester, holder)
	}
	if req.Message == nil || *req.Message != message {
		t.Errorf("Message = %v, want %q", req.Message, message)
	}
	if _, ok := requests.requests[req.ID]; !ok {
		t.Error("Request did not persist the return_request row")
	}

	if len(events.events) != 1 {
		t.Fatalf("Request appended %d events, want exactly 1", len(events.events))
	}
	if got := events.events[0]; got.Kind != domain.EventReturnRequested || got.ActorUserID != requester {
		t.Errorf("event = %+v, want Kind %v, ActorUserID %v", got, domain.EventReturnRequested, requester)
	}

	if len(notifier.requested) != 1 {
		t.Fatalf("notifier.ReturnRequested called %d times, want exactly 1", len(notifier.requested))
	}
	if n := notifier.requested[0]; n.RequestID != req.ID || n.RequesterLabel != "Riley" || n.HolderID != holder {
		t.Errorf("notification = %+v, want RequestID %v, RequesterLabel %q, HolderID %v", n, req.ID, "Riley", holder)
	}
}

func TestReturnRequestService_Request_IntegrationPrincipalRejected(t *testing.T) {
	itemGetter := &fakeReturnRequestItemGetter{item: checkedOutItem(identity.NewUserID())}
	svc, requests, events, _ := newTestReturnRequestService(itemGetter)
	actor := identity.NewIntegrationPrincipal("Nestova")

	_, err := svc.Request(context.Background(), actor, itemGetter.item.ID, nil)
	if !errors.Is(err, domain.ErrHolderRequired) {
		t.Errorf("Request(integration principal) = %v, want ErrHolderRequired", err)
	}
	if len(requests.requests) != 0 || len(events.events) != 0 {
		t.Error("rejected Request must not persist a request or an event")
	}
}

func TestReturnRequestService_Request_InvalidMessageRejected(t *testing.T) {
	itemGetter := &fakeReturnRequestItemGetter{item: checkedOutItem(identity.NewUserID())}
	svc, _, _, _ := newTestReturnRequestService(itemGetter)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Riley")
	blank := "   "

	_, err := svc.Request(context.Background(), actor, itemGetter.item.ID, &blank)
	if !errors.Is(err, domain.ErrInvalidReturnRequestMessage) {
		t.Errorf("Request(blank message) = %v, want ErrInvalidReturnRequestMessage", err)
	}
}

func TestReturnRequestService_Request_InvisibleItemMasked(t *testing.T) {
	itemGetter := &fakeReturnRequestItemGetter{getErr: domain.ErrItemNotFound}
	svc, _, _, _ := newTestReturnRequestService(itemGetter)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Riley")

	_, err := svc.Request(context.Background(), actor, domain.NewItemID(), nil)
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Request(invisible item) = %v, want wrapped ErrItemNotFound", err)
	}
}

func TestReturnRequestService_Request_ItemInBinRejected(t *testing.T) {
	itemGetter := &fakeReturnRequestItemGetter{item: binnedItemFixture()}
	svc, _, _, _ := newTestReturnRequestService(itemGetter)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Riley")

	_, err := svc.Request(context.Background(), actor, itemGetter.item.ID, nil)
	if !errors.Is(err, domain.ErrItemNotCheckedOut) {
		t.Errorf("Request(item in a bin) = %v, want ErrItemNotCheckedOut", err)
	}
}

func TestReturnRequestService_Request_HolderRejected(t *testing.T) {
	holder := identity.NewUserID()
	itemGetter := &fakeReturnRequestItemGetter{item: checkedOutItem(holder)}
	svc, _, _, _ := newTestReturnRequestService(itemGetter)
	actor := identity.NewUserPrincipal(holder, identity.RoleAdult, "Holder")

	_, err := svc.Request(context.Background(), actor, itemGetter.item.ID, nil)
	if !errors.Is(err, domain.ErrRequesterHoldsItem) {
		t.Errorf("Request(by the holder) = %v, want ErrRequesterHoldsItem", err)
	}
}

func TestReturnRequestService_Request_DuplicateOpenRejected(t *testing.T) {
	holder := identity.NewUserID()
	itemGetter := &fakeReturnRequestItemGetter{item: checkedOutItem(holder)}
	svc, _, _, notifier := newTestReturnRequestService(itemGetter)
	requester := identity.NewUserID()
	actor := identity.NewUserPrincipal(requester, identity.RoleAdult, "Riley")

	if _, err := svc.Request(context.Background(), actor, itemGetter.item.ID, nil); err != nil {
		t.Fatalf("first Request: %v", err)
	}

	_, err := svc.Request(context.Background(), actor, itemGetter.item.ID, nil)
	if !errors.Is(err, domain.ErrDuplicateReturnRequest) {
		t.Errorf("second Request(same requester) = %v, want ErrDuplicateReturnRequest", err)
	}
	if len(notifier.requested) != 1 {
		t.Errorf("notifier.ReturnRequested called %d times, want exactly 1 (only the first, successful Request)", len(notifier.requested))
	}
}

func TestReturnRequestService_Request_EventAppendFailureAbortsRequest(t *testing.T) {
	itemGetter := &fakeReturnRequestItemGetter{item: checkedOutItem(identity.NewUserID())}
	requests := newFakeReturnRequestRepo()
	events := &fakeEventAppender{appendErr: errors.New("boom")}
	uow := &fakeReturnRequestUOW{requests: requests, events: events}
	notifier := &fakeReturnRequestNotifier{}
	svc := app.NewReturnRequestService(itemGetter, requests, uow, notifier, testLogger())
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Riley")

	_, err := svc.Request(context.Background(), actor, itemGetter.item.ID, nil)
	if err == nil {
		t.Fatal("Request with a failing EventAppender = nil error, want one")
	}
	if len(requests.requests) != 0 {
		t.Error("a failing Append must roll back the return_request row too (no partial write)")
	}
	if len(notifier.requested) != 0 {
		t.Error("a rolled-back Request must never notify")
	}
}

func TestReturnRequestService_Cancel_Success(t *testing.T) {
	holder := identity.NewUserID()
	itemGetter := &fakeReturnRequestItemGetter{item: checkedOutItem(holder)}
	svc, requests, events, _ := newTestReturnRequestService(itemGetter)
	requester := identity.NewUserID()
	actor := identity.NewUserPrincipal(requester, identity.RoleAdult, "Riley")

	req, err := svc.Request(context.Background(), actor, itemGetter.item.ID, nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	if err := svc.Cancel(context.Background(), actor, itemGetter.item.ID, req.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if requests.requests[req.ID].Status != domain.ReturnRequestStatusCancelled {
		t.Errorf("Status = %v, want cancelled", requests.requests[req.ID].Status)
	}
	if requests.requests[req.ID].ResolvedAt == nil {
		t.Error("Cancel must set ResolvedAt")
	}

	if len(events.events) != 2 {
		t.Fatalf("Request+Cancel appended %d events, want exactly 2", len(events.events))
	}
	if got := events.events[1]; got.Kind != domain.EventReturnRequestCancelled {
		t.Errorf("event.Kind = %v, want %v", got.Kind, domain.EventReturnRequestCancelled)
	}
}

func TestReturnRequestService_Cancel_InvisibleItemMasked(t *testing.T) {
	itemGetter := &fakeReturnRequestItemGetter{getErr: domain.ErrItemNotFound}
	svc, _, _, _ := newTestReturnRequestService(itemGetter)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Riley")

	err := svc.Cancel(context.Background(), actor, domain.NewItemID(), domain.NewReturnRequestID())
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Cancel(invisible item) = %v, want wrapped ErrItemNotFound", err)
	}
}

func TestReturnRequestService_Cancel_UnknownRequestNotFound(t *testing.T) {
	itemGetter := &fakeReturnRequestItemGetter{item: checkedOutItem(identity.NewUserID())}
	svc, _, _, _ := newTestReturnRequestService(itemGetter)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Riley")

	err := svc.Cancel(context.Background(), actor, itemGetter.item.ID, domain.NewReturnRequestID())
	if !errors.Is(err, domain.ErrReturnRequestNotFound) {
		t.Errorf("Cancel(unknown request) = %v, want ErrReturnRequestNotFound", err)
	}
}

// TestReturnRequestService_Cancel_StrangerMasked proves ownership is
// enforced: a request raised by one user cannot be cancelled by another —
// the mismatch is masked as ErrReturnRequestNotFound, the same "not found"
// treatment an invisible item gets, never a distinguishable "forbidden".
func TestReturnRequestService_Cancel_StrangerMasked(t *testing.T) {
	holder := identity.NewUserID()
	itemGetter := &fakeReturnRequestItemGetter{item: checkedOutItem(holder)}
	svc, _, _, _ := newTestReturnRequestService(itemGetter)
	requester := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Riley")
	stranger := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Stranger")

	req, err := svc.Request(context.Background(), requester, itemGetter.item.ID, nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	err = svc.Cancel(context.Background(), stranger, itemGetter.item.ID, req.ID)
	if !errors.Is(err, domain.ErrReturnRequestNotFound) {
		t.Errorf("Cancel(by a stranger) = %v, want ErrReturnRequestNotFound", err)
	}
}

func TestReturnRequestService_Cancel_AlreadyResolvedRejected(t *testing.T) {
	holder := identity.NewUserID()
	itemGetter := &fakeReturnRequestItemGetter{item: checkedOutItem(holder)}
	svc, _, _, _ := newTestReturnRequestService(itemGetter)
	requester := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Riley")

	req, err := svc.Request(context.Background(), requester, itemGetter.item.ID, nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if err := svc.Cancel(context.Background(), requester, itemGetter.item.ID, req.ID); err != nil {
		t.Fatalf("first Cancel: %v", err)
	}

	err = svc.Cancel(context.Background(), requester, itemGetter.item.ID, req.ID)
	if !errors.Is(err, domain.ErrReturnRequestNotOpen) {
		t.Errorf("second Cancel(already cancelled) = %v, want ErrReturnRequestNotOpen", err)
	}
}

func TestReturnRequestService_ListForItem(t *testing.T) {
	holder := identity.NewUserID()
	itemGetter := &fakeReturnRequestItemGetter{item: checkedOutItem(holder)}
	svc, _, _, _ := newTestReturnRequestService(itemGetter)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Riley")

	if _, err := svc.Request(context.Background(), actor, itemGetter.item.ID, nil); err != nil {
		t.Fatalf("Request: %v", err)
	}

	reqs, err := svc.ListForItem(context.Background(), actor, itemGetter.item.ID)
	if err != nil {
		t.Fatalf("ListForItem: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("ListForItem = %d requests, want exactly 1", len(reqs))
	}
}

func TestReturnRequestService_ListForItem_InvisibleItemMasked(t *testing.T) {
	itemGetter := &fakeReturnRequestItemGetter{getErr: domain.ErrItemNotFound}
	svc, _, _, _ := newTestReturnRequestService(itemGetter)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Riley")

	_, err := svc.ListForItem(context.Background(), actor, domain.NewItemID())
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("ListForItem(invisible item) = %v, want wrapped ErrItemNotFound", err)
	}
}
