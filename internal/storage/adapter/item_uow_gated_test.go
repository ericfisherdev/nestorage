package adapter_test

import (
	"errors"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/adapter"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// newItemService wires an app.ItemService against real Postgres via f's own
// pool: a PostgresUnitOfWork for the WithinItemTx seam Create/Edit/Delete
// run through, and f.repo directly for the non-transactional Get/ListInBin
// reads — mirrors newOperationService/newBinMover's own wiring rationale
// (operations_gated_test.go/mover_gated_test.go).
func newItemService(f *itemFixture) *app.ItemService {
	uow := adapter.NewPostgresUnitOfWork(f.pool)
	return app.NewItemService(f.repo, uow, testOpsLogger())
}

// TestDeletedItemEventSurvivesItemDelete guards the no-cascading-FK
// contract NSTR-41 shares with NSTR-40: item_event.item_id carries no
// foreign key to item specifically so a 'deleted' event, written inside the
// same transaction that removes the item row, survives that removal (see
// 00012_item_event.sql's own comment).
func TestDeletedItemEventSurvivesItemDelete(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleAdult)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem(f.household, "Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	svc := newItemService(f)
	actor := identity.NewUserPrincipal(creator, identity.RoleAdult, "Creator")
	actor.HouseholdID = f.household

	if err := svc.Delete(testCtx(t), it.ID, actor); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleAdult, "Creator")
	viewer.HouseholdID = f.household
	if _, err := f.repo.Get(testCtx(t), viewer, it.ID); !errors.Is(err, domain.ErrItemNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrItemNotFound", err)
	}

	events, err := f.events.ListByItem(testCtx(t), f.household, it.ID, domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByItem after Delete: %v", err)
	}
	if len(events) != 1 || events[0].Kind != domain.EventDeleted {
		t.Fatalf("ListByItem after Delete = %+v, want exactly one deleted event surviving the item's own removal", events)
	}
	if events[0].ItemName != "Stove" {
		t.Errorf("deleted event ItemName = %q, want %q (snapshot before the row was removed)", events[0].ItemName, "Stove")
	}
}

// TestCreateAndEventCommitTogether proves NSTR-41's atomicity contract for
// ItemService.Create over a real pgx transaction: a successful Create
// leaves both the item row and exactly one created event.
func TestCreateAndEventCommitTogether(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleAdult)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)

	svc := newItemService(f)
	actor := identity.NewUserPrincipal(creator, identity.RoleAdult, "Creator")
	actor.HouseholdID = f.household

	it, err := svc.Create(testCtx(t), "Stove", nil, 1, bin, actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleAdult, "Creator")
	viewer.HouseholdID = f.household
	if _, err := f.repo.Get(testCtx(t), viewer, it.ID); err != nil {
		t.Fatalf("Get after Create: %v", err)
	}

	events, err := f.events.ListByItem(testCtx(t), f.household, it.ID, domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(events) != 1 || events[0].Kind != domain.EventCreated {
		t.Fatalf("ListByItem = %+v, want exactly one created event", events)
	}
}

// TestEditAndEventCommitTogether proves NSTR-41's atomicity contract for
// ItemService.Edit over a real pgx transaction: a field change that
// actually differs leaves both the updated row and exactly one edited
// event naming the changed field.
func TestEditAndEventCommitTogether(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleAdult)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem(f.household, "Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	svc := newItemService(f)
	actor := identity.NewUserPrincipal(creator, identity.RoleAdult, "Creator")
	actor.HouseholdID = f.household

	if err := svc.Edit(testCtx(t), it.ID, "Camping stove", nil, 2, actor); err != nil {
		t.Fatalf("Edit: %v", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleAdult, "Creator")
	viewer.HouseholdID = f.household
	got, err := f.repo.Get(testCtx(t), viewer, it.ID)
	if err != nil {
		t.Fatalf("Get after Edit: %v", err)
	}
	if got.Name != "Camping stove" || got.Quantity != 2 {
		t.Errorf("Edit did not persist: %+v", got)
	}

	events, err := f.events.ListByItem(testCtx(t), f.household, it.ID, domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(events) != 1 || events[0].Kind != domain.EventEdited {
		t.Fatalf("ListByItem = %+v, want exactly one edited event", events)
	}
	want := []domain.EditedField{domain.FieldName, domain.FieldQuantity}
	if len(events[0].ChangedFields) != len(want) {
		t.Fatalf("ChangedFields = %v, want %v", events[0].ChangedFields, want)
	}
	for i, field := range want {
		if events[0].ChangedFields[i] != field {
			t.Errorf("ChangedFields[%d] = %v, want %v", i, events[0].ChangedFields[i], field)
		}
	}
}

// TestEditAndEventRollBackTogether is the sibling of
// TestOperationAndEventRollBackTogether for the item-lifecycle seam: Edit
// against an unknown item id fails GetForUpdate before Update/Append ever
// run, so no item row is created and no event is left behind — the real
// pgx transaction rolls back with nothing to undo, which is itself the
// proof there is nothing partially committed.
func TestEditAndEventRollBackTogether(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleAdult)
	svc := newItemService(f)
	actor := identity.NewUserPrincipal(creator, identity.RoleAdult, "Creator")
	actor.HouseholdID = f.household
	unknownID := domain.NewItemID()

	err := svc.Edit(testCtx(t), unknownID, "Stove", nil, 1, actor)
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Fatalf("Edit(unknown item) = %v, want wrapped ErrItemNotFound", err)
	}

	events, err := f.events.ListByItem(testCtx(t), f.household, unknownID, domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("rejected Edit must leave no event row: got %+v", events)
	}
}
