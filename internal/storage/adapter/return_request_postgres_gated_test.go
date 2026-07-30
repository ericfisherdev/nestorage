package adapter_test

import (
	"errors"
	"testing"
	"time"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/adapter"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// Every test below reuses newItemFixture/newItem from
// item_postgres_gated_test.go (same package, same "storage" derived
// database — dbtest.Harness.NewIsolatedPool must be called exactly once per
// test) rather than standing up a second, parallel fixture type just to add
// one more repository to it, mirroring item_link_postgres_gated_test.go's
// own doc.

func TestNewReturnRequestRepository_NilExecutorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewReturnRequestRepository(nil) did not panic")
		}
	}()
	adapter.NewReturnRequestRepository(nil)
}

// seedCheckedOutItem creates and persists an item held by holder — the
// starting state every return-request test below needs, since a return
// request only ever exists against a checked-out item.
func seedCheckedOutItem(t *testing.T, f *itemFixture, creator, holder identity.UserID) *domain.Item {
	t.Helper()
	it := &domain.Item{ID: domain.NewItemID(), Name: "Stove", Quantity: 1, HeldBy: &holder, CreatedBy: creator}
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("seed checked-out item: %v", err)
	}
	return it
}

func TestReturnRequestRepository_Create(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleAdult)
	holder := f.seedUser(t, identity.RoleAdult)
	requester := f.seedUser(t, identity.RoleAdult)
	it := seedCheckedOutItem(t, f, creator, holder)

	message := "please, I need this back"
	req := &domain.ReturnRequest{
		ID: domain.NewReturnRequestID(), ItemID: it.ID,
		RequesterID: requester, HolderID: holder, Message: &message, Status: domain.ReturnRequestStatusOpen,
	}
	if err := f.requests.Create(testCtx(t), req); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if req.CreatedAt.IsZero() {
		t.Error("Create left CreatedAt zero")
	}

	got, err := f.requests.ListByItem(testCtx(t), it.ID)
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(got) != 1 || got[0].Status != domain.ReturnRequestStatusOpen || got[0].Message == nil || *got[0].Message != message {
		t.Errorf("ListByItem = %+v, want exactly the created open request with its message", got)
	}
}

// TestReturnRequestRepository_Create_DuplicateOpenRejected proves the
// return_request_open_uniq partial unique index: a second open request from
// the SAME requester on the SAME item is rejected.
func TestReturnRequestRepository_Create_DuplicateOpenRejected(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleAdult)
	holder := f.seedUser(t, identity.RoleAdult)
	requester := f.seedUser(t, identity.RoleAdult)
	it := seedCheckedOutItem(t, f, creator, holder)

	first := &domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: it.ID, RequesterID: requester, HolderID: holder, Status: domain.ReturnRequestStatusOpen}
	if err := f.requests.Create(testCtx(t), first); err != nil {
		t.Fatalf("Create(first): %v", err)
	}

	second := &domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: it.ID, RequesterID: requester, HolderID: holder, Status: domain.ReturnRequestStatusOpen}
	err := f.requests.Create(testCtx(t), second)
	if !errors.Is(err, domain.ErrDuplicateReturnRequest) {
		t.Errorf("Create(duplicate open) = %v, want ErrDuplicateReturnRequest", err)
	}
}

// TestReturnRequestRepository_Create_AllowsReRequestAfterResolution proves
// the partial index's own predicate: once the first request is no longer
// open, the same requester may raise a new one on the same item.
func TestReturnRequestRepository_Create_AllowsReRequestAfterResolution(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleAdult)
	holder := f.seedUser(t, identity.RoleAdult)
	requester := f.seedUser(t, identity.RoleAdult)
	it := seedCheckedOutItem(t, f, creator, holder)

	first := &domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: it.ID, RequesterID: requester, HolderID: holder, Status: domain.ReturnRequestStatusOpen}
	if err := f.requests.Create(testCtx(t), first); err != nil {
		t.Fatalf("Create(first): %v", err)
	}
	if _, err := f.requests.Cancel(testCtx(t), first.ID, requester); err != nil {
		t.Fatalf("Cancel(first): %v", err)
	}

	second := &domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: it.ID, RequesterID: requester, HolderID: holder, Status: domain.ReturnRequestStatusOpen}
	if err := f.requests.Create(testCtx(t), second); err != nil {
		t.Errorf("Create(after cancellation) = %v, want nil", err)
	}
}

func TestReturnRequestRepository_Create_UnknownItemRejected(t *testing.T) {
	f := newItemFixture(t)
	holder := f.seedUser(t, identity.RoleAdult)
	requester := f.seedUser(t, identity.RoleAdult)

	req := &domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: domain.NewItemID(), RequesterID: requester, HolderID: holder, Status: domain.ReturnRequestStatusOpen}
	err := f.requests.Create(testCtx(t), req)
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Create(unknown item) = %v, want ErrItemNotFound", err)
	}
}

func TestReturnRequestRepository_ListByItem_NewestFirst(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleAdult)
	holder := f.seedUser(t, identity.RoleAdult)
	it := seedCheckedOutItem(t, f, creator, holder)

	first := &domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: it.ID, RequesterID: f.seedUser(t, identity.RoleAdult), HolderID: holder, Status: domain.ReturnRequestStatusOpen}
	if err := f.requests.Create(testCtx(t), first); err != nil {
		t.Fatalf("Create(first): %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	second := &domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: it.ID, RequesterID: f.seedUser(t, identity.RoleAdult), HolderID: holder, Status: domain.ReturnRequestStatusOpen}
	if err := f.requests.Create(testCtx(t), second); err != nil {
		t.Fatalf("Create(second): %v", err)
	}

	got, err := f.requests.ListByItem(testCtx(t), it.ID)
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(got) != 2 || got[0].ID != second.ID || got[1].ID != first.ID {
		t.Errorf("ListByItem order = %+v, want [second, first] (newest first)", got)
	}
}

func TestReturnRequestRepository_ListByItem_Empty(t *testing.T) {
	f := newItemFixture(t)
	got, err := f.requests.ListByItem(testCtx(t), domain.NewItemID())
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByItem(no requests) = %+v, want empty slice", got)
	}
}

func TestReturnRequestRepository_Cancel(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleAdult)
	holder := f.seedUser(t, identity.RoleAdult)
	requester := f.seedUser(t, identity.RoleAdult)
	it := seedCheckedOutItem(t, f, creator, holder)

	req := &domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: it.ID, RequesterID: requester, HolderID: holder, Status: domain.ReturnRequestStatusOpen}
	if err := f.requests.Create(testCtx(t), req); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := f.requests.Cancel(testCtx(t), req.ID, requester)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got.Status != domain.ReturnRequestStatusCancelled || got.ResolvedAt == nil {
		t.Errorf("Cancel returned = %+v, want status cancelled with ResolvedAt set", got)
	}
	if got.ItemID != it.ID {
		t.Errorf("Cancel returned ItemID = %v, want %v", got.ItemID, it.ID)
	}
}

func TestReturnRequestRepository_Cancel_UnknownNotFound(t *testing.T) {
	f := newItemFixture(t)
	requester := f.seedUser(t, identity.RoleAdult)

	_, err := f.requests.Cancel(testCtx(t), domain.NewReturnRequestID(), requester)
	if !errors.Is(err, domain.ErrReturnRequestNotFound) {
		t.Errorf("Cancel(unknown) = %v, want ErrReturnRequestNotFound", err)
	}
}

// TestReturnRequestRepository_Cancel_WrongRequesterMasked proves ownership
// is enforced in the SQL itself: a request belonging to someone else is
// indistinguishable from an unknown one.
func TestReturnRequestRepository_Cancel_WrongRequesterMasked(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleAdult)
	holder := f.seedUser(t, identity.RoleAdult)
	requester := f.seedUser(t, identity.RoleAdult)
	stranger := f.seedUser(t, identity.RoleAdult)
	it := seedCheckedOutItem(t, f, creator, holder)

	req := &domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: it.ID, RequesterID: requester, HolderID: holder, Status: domain.ReturnRequestStatusOpen}
	if err := f.requests.Create(testCtx(t), req); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err := f.requests.Cancel(testCtx(t), req.ID, stranger)
	if !errors.Is(err, domain.ErrReturnRequestNotFound) {
		t.Errorf("Cancel(wrong requester) = %v, want ErrReturnRequestNotFound", err)
	}

	got, err := f.requests.ListByItem(testCtx(t), it.ID)
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(got) != 1 || got[0].Status != domain.ReturnRequestStatusOpen {
		t.Error("a rejected Cancel by a stranger must leave the request open")
	}
}

func TestReturnRequestRepository_Cancel_AlreadyResolvedRejected(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleAdult)
	holder := f.seedUser(t, identity.RoleAdult)
	requester := f.seedUser(t, identity.RoleAdult)
	it := seedCheckedOutItem(t, f, creator, holder)

	req := &domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: it.ID, RequesterID: requester, HolderID: holder, Status: domain.ReturnRequestStatusOpen}
	if err := f.requests.Create(testCtx(t), req); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.requests.Cancel(testCtx(t), req.ID, requester); err != nil {
		t.Fatalf("first Cancel: %v", err)
	}

	_, err := f.requests.Cancel(testCtx(t), req.ID, requester)
	if !errors.Is(err, domain.ErrReturnRequestNotOpen) {
		t.Errorf("second Cancel(already cancelled) = %v, want ErrReturnRequestNotOpen", err)
	}
}

// TestReturnRequestRepository_FulfillOpenForItem proves FulfillOpenForItem
// flips EXACTLY the open rows on itemID: two open requests on the target
// item both flip, a third, already-cancelled request on the same item does
// not, and a fourth, open request on a DIFFERENT item is untouched.
func TestReturnRequestRepository_FulfillOpenForItem(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleAdult)
	holder := f.seedUser(t, identity.RoleAdult)
	it := seedCheckedOutItem(t, f, creator, holder)
	otherIt := seedCheckedOutItem(t, f, creator, holder)

	openA := &domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: it.ID, RequesterID: f.seedUser(t, identity.RoleAdult), HolderID: holder, Status: domain.ReturnRequestStatusOpen}
	openB := &domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: it.ID, RequesterID: f.seedUser(t, identity.RoleAdult), HolderID: holder, Status: domain.ReturnRequestStatusOpen}
	cancelled := &domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: it.ID, RequesterID: f.seedUser(t, identity.RoleAdult), HolderID: holder, Status: domain.ReturnRequestStatusOpen}
	otherItemOpen := &domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: otherIt.ID, RequesterID: f.seedUser(t, identity.RoleAdult), HolderID: holder, Status: domain.ReturnRequestStatusOpen}
	for _, r := range []*domain.ReturnRequest{openA, openB, cancelled, otherItemOpen} {
		if err := f.requests.Create(testCtx(t), r); err != nil {
			t.Fatalf("seed request: %v", err)
		}
	}
	if _, err := f.requests.Cancel(testCtx(t), cancelled.ID, cancelled.RequesterID); err != nil {
		t.Fatalf("Cancel(cancelled): %v", err)
	}

	at := time.Now().UTC().Truncate(time.Microsecond)
	fulfilled, err := f.requests.FulfillOpenForItem(testCtx(t), it.ID, at)
	if err != nil {
		t.Fatalf("FulfillOpenForItem: %v", err)
	}
	if len(fulfilled) != 2 {
		t.Fatalf("FulfillOpenForItem returned %d rows, want exactly 2", len(fulfilled))
	}
	for _, r := range fulfilled {
		if r.Status != domain.ReturnRequestStatusFulfilled || r.ResolvedAt == nil || !r.ResolvedAt.Equal(at) {
			t.Errorf("fulfilled row = %+v, want status fulfilled with ResolvedAt %v", r, at)
		}
	}

	got, err := f.requests.ListByItem(testCtx(t), it.ID)
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	statuses := make(map[domain.ReturnRequestID]domain.ReturnRequestStatus, len(got))
	for _, r := range got {
		statuses[r.ID] = r.Status
	}
	if statuses[openA.ID] != domain.ReturnRequestStatusFulfilled || statuses[openB.ID] != domain.ReturnRequestStatusFulfilled {
		t.Errorf("open requests not flipped: %+v", statuses)
	}
	if statuses[cancelled.ID] != domain.ReturnRequestStatusCancelled {
		t.Errorf("an already-cancelled request must stay cancelled, got %v", statuses[cancelled.ID])
	}

	otherGot, err := f.requests.ListByItem(testCtx(t), otherIt.ID)
	if err != nil {
		t.Fatalf("ListByItem(other item): %v", err)
	}
	if len(otherGot) != 1 || otherGot[0].Status != domain.ReturnRequestStatusOpen {
		t.Error("FulfillOpenForItem must not touch a different item's open request")
	}
}

func TestReturnRequestRepository_FulfillOpenForItem_Empty(t *testing.T) {
	f := newItemFixture(t)
	fulfilled, err := f.requests.FulfillOpenForItem(testCtx(t), domain.NewItemID(), time.Now())
	if err != nil {
		t.Fatalf("FulfillOpenForItem: %v", err)
	}
	if len(fulfilled) != 0 {
		t.Errorf("FulfillOpenForItem(no open requests) = %+v, want empty slice", fulfilled)
	}
}

// TestReturnRequestRepository_CascadeDeleteWithItem proves item_id's ON
// DELETE CASCADE: removing the owning item removes its return requests with
// it, with no separate cleanup step.
func TestReturnRequestRepository_CascadeDeleteWithItem(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleAdult)
	holder := f.seedUser(t, identity.RoleAdult)
	requester := f.seedUser(t, identity.RoleAdult)
	it := seedCheckedOutItem(t, f, creator, holder)

	req := &domain.ReturnRequest{ID: domain.NewReturnRequestID(), ItemID: it.ID, RequesterID: requester, HolderID: holder, Status: domain.ReturnRequestStatusOpen}
	if err := f.requests.Create(testCtx(t), req); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := f.repo.Delete(testCtx(t), it.ID); err != nil {
		t.Fatalf("Delete(item): %v", err)
	}

	got, err := f.requests.ListByItem(testCtx(t), it.ID)
	if err != nil {
		t.Fatalf("ListByItem after item cascade delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByItem after item delete = %+v, want empty (cascade)", got)
	}
}
