package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// fakeItemRepo is an in-memory fake satisfying BOTH of ItemService's ports:
// itemRepository (Get/ListByBin, the non-transactional reads) and
// ItemLifecycleStore (Create/GetForUpdate/Update/Delete, the tx-bound
// writes) — mirroring how a single *adapter.ItemRepository backs both in
// production, just bound to the pool for one and a pgx.Tx for the other.
// Get/GetForUpdate return a copy so a caller's in-place mutation (Edit's
// read-modify-write) can never corrupt the fake's own stored state ahead of
// Update, the same rationale fakeItemStore's own doc gives.
type fakeItemRepo struct {
	items map[domain.ItemID]*domain.Item

	createErr       error
	getErr          error
	getForUpdateErr error
	updateErr       error
	listErr         error
	listVisibleErr  error
	deleteErr       error
}

func newFakeItemRepo() *fakeItemRepo {
	return &fakeItemRepo{items: make(map[domain.ItemID]*domain.Item)}
}

func (f *fakeItemRepo) Create(_ context.Context, it *domain.Item) error {
	if f.createErr != nil {
		return f.createErr
	}
	cp := *it
	f.items[it.ID] = &cp
	return nil
}

func (f *fakeItemRepo) Get(_ context.Context, _ identity.Principal, id domain.ItemID) (*domain.Item, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	it, ok := f.items[id]
	if !ok {
		return nil, domain.ErrItemNotFound
	}
	cp := *it
	return &cp, nil
}

func (f *fakeItemRepo) GetForUpdate(_ context.Context, id domain.ItemID) (*domain.Item, error) {
	if f.getForUpdateErr != nil {
		return nil, f.getForUpdateErr
	}
	it, ok := f.items[id]
	if !ok {
		return nil, domain.ErrItemNotFound
	}
	cp := *it
	return &cp, nil
}

func (f *fakeItemRepo) Update(_ context.Context, it *domain.Item) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	existing, ok := f.items[it.ID]
	if !ok {
		return domain.ErrItemNotFound
	}
	existing.Name, existing.Description, existing.Quantity = it.Name, it.Description, it.Quantity
	return nil
}

func (f *fakeItemRepo) ListByBin(_ context.Context, _ identity.Principal, binID domain.BinID) ([]domain.Item, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	items := make([]domain.Item, 0)
	for _, it := range f.items {
		if it.CurrentBinID != nil && *it.CurrentBinID == binID {
			items = append(items, *it)
		}
	}
	return items, nil
}

// ListVisible mirrors ListByBin's own shape, additionally applying filter's
// BinID/HeldBy (both optional, AND-ed when both are set), ordered by name
// then id to match domain.ItemRepository.ListVisible's own documented order
// — the sort api_common.go's page() function depends on for callers wired
// through this fake.
func (f *fakeItemRepo) ListVisible(_ context.Context, _ identity.Principal, filter domain.ItemFilter) ([]domain.Item, error) {
	if f.listVisibleErr != nil {
		return nil, f.listVisibleErr
	}
	items := make([]domain.Item, 0)
	for _, it := range f.items {
		if filter.BinID != nil && (it.CurrentBinID == nil || *it.CurrentBinID != *filter.BinID) {
			continue
		}
		if filter.HeldBy != nil && (it.HeldBy == nil || *it.HeldBy != *filter.HeldBy) {
			continue
		}
		items = append(items, *it)
	}
	slices.SortFunc(items, func(a, b domain.Item) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.ID.String(), b.ID.String())
	})
	return items, nil
}

func (f *fakeItemRepo) Delete(_ context.Context, id domain.ItemID) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.items[id]; !ok {
		return domain.ErrItemNotFound
	}
	delete(f.items, id)
	return nil
}

// fakeItemTxUnitOfWork runs fn directly against a shared fakeItemRepo (as
// ItemTxStores.Items) and a fakeEventAppender, restoring the repo's items
// map to a pre-fn snapshot on error — mirrors fakeUnitOfWork/
// fakeBinUnitOfWork's own rollback simulation (operations_test.go/
// mover_test.go). A whole-map snapshot, not a single struct field, because
// Create/Delete change which keys exist, not just their values.
type fakeItemTxUnitOfWork struct {
	store  *fakeItemRepo
	events *fakeEventAppender
}

func (u *fakeItemTxUnitOfWork) WithinItemTx(_ context.Context, fn func(app.ItemTxStores) error) error {
	before := snapshotItems(u.store.items)
	err := fn(app.ItemTxStores{Items: u.store, Events: u.events})
	if err != nil {
		u.store.items = before
	}
	return err
}

func snapshotItems(items map[domain.ItemID]*domain.Item) map[domain.ItemID]*domain.Item {
	cp := make(map[domain.ItemID]*domain.Item, len(items))
	for id, it := range items {
		v := *it
		cp[id] = &v
	}
	return cp
}

// newTestItemService wires an ItemService over a fresh fakeItemRepo and
// fakeEventAppender, returning the appender alongside the service so a
// test that cares which events were appended can inspect it directly.
func newTestItemService(repo *fakeItemRepo) (*app.ItemService, *fakeEventAppender) {
	events := &fakeEventAppender{}
	uow := &fakeItemTxUnitOfWork{store: repo, events: events}
	return app.NewItemService(repo, uow, testLogger()), events
}

func strPtr(s string) *string { return &s }

func TestNewItemService_PanicsOnNilDeps(t *testing.T) {
	repo := newFakeItemRepo()
	uow := &fakeItemTxUnitOfWork{store: repo, events: &fakeEventAppender{}}

	tests := []struct {
		name  string
		build func()
	}{
		{"nil itemRepository", func() { app.NewItemService(nil, uow, testLogger()) }},
		{"nil itemTransactor", func() { app.NewItemService(repo, nil, testLogger()) }},
		{"nil logger", func() { app.NewItemService(repo, uow, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("NewItemService did not panic")
				}
			}()
			tt.build()
		})
	}
}

func TestItemService_Create(t *testing.T) {
	repo := newFakeItemRepo()
	svc, _ := newTestItemService(repo)
	binID := domain.NewBinID()
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

	it, err := svc.Create(context.Background(), "Camping stove", nil, 1, binID, actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if it.CurrentBinID == nil || *it.CurrentBinID != binID {
		t.Errorf("Create: CurrentBinID = %v, want %v", it.CurrentBinID, binID)
	}
	if it.CreatedBy != actor.UserID {
		t.Errorf("Create: CreatedBy = %v, want %v", it.CreatedBy, actor.UserID)
	}
	if _, ok := repo.items[it.ID]; !ok {
		t.Error("Create did not persist the item via the repository")
	}
}

// TestCreateEmitsCreatedEvent proves NSTR-41's per-operation event contract
// for ItemService.Create: exactly one EventCreated row, attributed to actor.
func TestCreateEmitsCreatedEvent(t *testing.T) {
	repo := newFakeItemRepo()
	svc, events := newTestItemService(repo)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

	it, err := svc.Create(context.Background(), "Stove", nil, 1, domain.NewBinID(), actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(events.events) != 1 {
		t.Fatalf("Create appended %d events, want exactly 1", len(events.events))
	}
	got := events.events[0]
	if got.Kind != domain.EventCreated {
		t.Errorf("event.Kind = %v, want %v", got.Kind, domain.EventCreated)
	}
	if got.ItemID != it.ID {
		t.Errorf("event.ItemID = %v, want %v", got.ItemID, it.ID)
	}
	if got.ActorLabel != actor.Actor() {
		t.Errorf("event.ActorLabel = %q, want %q", got.ActorLabel, actor.Actor())
	}
}

// TestCreateEventFailureAbortsCreate proves atomicity: a failing
// EventAppender makes Create return an error and leaves the fake store
// observing a rollback — the item row Create inserted is gone too, exactly
// as a real pgx transaction's rollback would undo it.
func TestCreateEventFailureAbortsCreate(t *testing.T) {
	repo := newFakeItemRepo()
	events := &fakeEventAppender{appendErr: errors.New("boom")}
	uow := &fakeItemTxUnitOfWork{store: repo, events: events}
	svc := app.NewItemService(repo, uow, testLogger())
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

	_, err := svc.Create(context.Background(), "Stove", nil, 1, domain.NewBinID(), actor)
	if err == nil {
		t.Fatal("Create with a failing EventAppender = nil error, want one")
	}
	if len(repo.items) != 0 {
		t.Error("a failing Append must roll back the insert too: no item row should remain")
	}
}

func TestItemService_Create_ValidationRejected(t *testing.T) {
	repo := newFakeItemRepo()
	svc, _ := newTestItemService(repo)
	binID := domain.NewBinID()
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

	tests := []struct {
		name     string
		itemName string
		quantity int
		wantErr  error
	}{
		{"blank name", "  ", 1, domain.ErrItemNameRequired},
		{"zero quantity", "Stove", 0, domain.ErrInvalidQuantity},
		{"negative quantity", "Stove", -1, domain.ErrInvalidQuantity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Create(context.Background(), tt.itemName, nil, tt.quantity, binID, actor)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Create(%q, %d) error = %v, want %v", tt.itemName, tt.quantity, err, tt.wantErr)
			}
			if len(repo.items) != 0 {
				t.Error("Create must not reach the repository when validation fails")
			}
		})
	}
}

func TestItemService_Create_RepositoryErrorWrapped(t *testing.T) {
	repo := newFakeItemRepo()
	repo.createErr = domain.ErrBinNotFound
	svc, _ := newTestItemService(repo)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

	_, err := svc.Create(context.Background(), "Stove", nil, 1, domain.NewBinID(), actor)
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("Create() error = %v, want wrapped ErrBinNotFound", err)
	}
}

func TestItemService_Edit(t *testing.T) {
	repo := newFakeItemRepo()
	svc, _ := newTestItemService(repo)
	binID := domain.NewBinID()
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")
	it, err := svc.Create(context.Background(), "Stove", nil, 1, binID, actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	desc := "Two-burner camping stove"
	if err := svc.Edit(context.Background(), it.ID, "Camping stove", &desc, 2, actor); err != nil {
		t.Fatalf("Edit: %v", err)
	}

	got := repo.items[it.ID]
	if got.Name != "Camping stove" || got.Description == nil || *got.Description != desc || got.Quantity != 2 {
		t.Errorf("Edit did not update the item: %+v", got)
	}
	if got.CurrentBinID == nil || *got.CurrentBinID != binID {
		t.Error("Edit must never touch placement")
	}
}

// TestEditEmitsEditedEventWithChangedFields is table-driven across which
// editable fields actually differ — NSTR-41's Sprint 6 decision:
// ChangedFields names precisely the fields that differed, no more no less.
func TestEditEmitsEditedEventWithChangedFields(t *testing.T) {
	baseDesc := "original description"
	tests := []struct {
		name        string
		newName     string
		newDesc     *string
		newQuantity int
		want        []domain.EditedField
	}{
		{"name only", "Renamed stove", strPtr(baseDesc), 1, []domain.EditedField{domain.FieldName}},
		{"quantity only", "Stove", strPtr(baseDesc), 5, []domain.EditedField{domain.FieldQuantity}},
		{"description only", "Stove", strPtr("new description"), 1, []domain.EditedField{domain.FieldDescription}},
		{"all three", "Renamed", strPtr("new description"), 9, []domain.EditedField{domain.FieldName, domain.FieldDescription, domain.FieldQuantity}},
		{"description set to nil", "Stove", nil, 1, []domain.EditedField{domain.FieldDescription}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeItemRepo()
			svc, events := newTestItemService(repo)
			actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")
			it, err := svc.Create(context.Background(), "Stove", strPtr(baseDesc), 1, domain.NewBinID(), actor)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			if err := svc.Edit(context.Background(), it.ID, tt.newName, tt.newDesc, tt.newQuantity, actor); err != nil {
				t.Fatalf("Edit: %v", err)
			}

			if len(events.events) != 2 {
				t.Fatalf("expected 2 events (created + edited), got %d: %+v", len(events.events), events.events)
			}
			got := events.events[1]
			if got.Kind != domain.EventEdited {
				t.Fatalf("Kind = %v, want %v", got.Kind, domain.EventEdited)
			}
			if !slices.Equal(got.ChangedFields, tt.want) {
				t.Errorf("ChangedFields = %v, want %v", got.ChangedFields, tt.want)
			}
		})
	}
}

// TestEditWithNoChangesEmitsNoEvent proves the Sprint 6 decision's negative
// case: resubmitting identical values still writes (Update runs
// unconditionally) but appends no event — "the log records changes, not
// requests to change."
func TestEditWithNoChangesEmitsNoEvent(t *testing.T) {
	repo := newFakeItemRepo()
	svc, events := newTestItemService(repo)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")
	desc := "same description"
	it, err := svc.Create(context.Background(), "Stove", &desc, 1, domain.NewBinID(), actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := svc.Edit(context.Background(), it.ID, "Stove", &desc, 1, actor); err != nil {
		t.Fatalf("Edit: %v", err)
	}

	if len(events.events) != 1 {
		t.Errorf("Edit with no changes appended %d events beyond Create's, want 0", len(events.events)-1)
	}
}

// TestEditEventFailureAbortsEdit proves atomicity: a failing EventAppender
// makes Edit return an error and leaves the fake store observing a
// rollback — the field change Update already wrote does not land.
func TestEditEventFailureAbortsEdit(t *testing.T) {
	repo := newFakeItemRepo()
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")
	svc, events := newTestItemService(repo)
	it, err := svc.Create(context.Background(), "Stove", nil, 1, domain.NewBinID(), actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	events.appendErr = errors.New("boom")

	newDesc := "renamed"
	if err := svc.Edit(context.Background(), it.ID, "Renamed stove", &newDesc, 2, actor); err == nil {
		t.Fatal("Edit with a failing EventAppender = nil error, want one")
	}

	got := repo.items[it.ID]
	if got.Name != "Stove" || got.Quantity != 1 {
		t.Errorf("a failing Append must roll back the field change too: got %+v", got)
	}
}

// TestEditEventAttribution is table-driven across the three credential
// kinds, mirroring TestOperationService_EventAttribution's own rationale
// for why "session" and "device-token" exercise the same code path.
func TestEditEventAttribution(t *testing.T) {
	tests := []struct {
		name  string
		actor identity.Principal
	}{
		{"session principal", identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")},
		{"device-token principal", identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Bob")},
		{"integration principal", identity.NewIntegrationPrincipal("Nestova")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeItemRepo()
			svc, events := newTestItemService(repo)
			it, err := svc.Create(context.Background(), "Stove", nil, 1, domain.NewBinID(), tt.actor)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			if err := svc.Edit(context.Background(), it.ID, "Renamed", nil, 1, tt.actor); err != nil {
				t.Fatalf("Edit: %v", err)
			}

			edited := events.events[len(events.events)-1]
			if edited.ActorKind != tt.actor.Kind {
				t.Errorf("ActorKind = %v, want %v", edited.ActorKind, tt.actor.Kind)
			}
			if edited.ActorUserID != tt.actor.UserID {
				t.Errorf("ActorUserID = %v, want %v", edited.ActorUserID, tt.actor.UserID)
			}
			if edited.ActorLabel != tt.actor.Actor() {
				t.Errorf("ActorLabel = %q, want %q", edited.ActorLabel, tt.actor.Actor())
			}
		})
	}
}

func TestItemService_Edit_ValidationRejected(t *testing.T) {
	repo := newFakeItemRepo()
	svc, _ := newTestItemService(repo)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

	if err := svc.Edit(context.Background(), domain.NewItemID(), "", nil, 1, actor); !errors.Is(err, domain.ErrItemNameRequired) {
		t.Errorf("Edit(blank name) error = %v, want ErrItemNameRequired", err)
	}
	if err := svc.Edit(context.Background(), domain.NewItemID(), "Stove", nil, 0, actor); !errors.Is(err, domain.ErrInvalidQuantity) {
		t.Errorf("Edit(zero quantity) error = %v, want ErrInvalidQuantity", err)
	}
}

func TestItemService_Edit_NotFoundWrapped(t *testing.T) {
	repo := newFakeItemRepo()
	svc, _ := newTestItemService(repo)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

	err := svc.Edit(context.Background(), domain.NewItemID(), "Stove", nil, 1, actor)
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Edit(unknown) error = %v, want wrapped ErrItemNotFound", err)
	}
}

func TestItemService_Get(t *testing.T) {
	repo := newFakeItemRepo()
	svc, _ := newTestItemService(repo)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

	it, err := svc.Create(context.Background(), "Stove", nil, 1, domain.NewBinID(), actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.Get(context.Background(), viewer, it.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != it.ID {
		t.Errorf("Get() = %v, want %v", got.ID, it.ID)
	}
}

func TestItemService_Get_NotFoundWrapped(t *testing.T) {
	repo := newFakeItemRepo()
	svc, _ := newTestItemService(repo)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	_, err := svc.Get(context.Background(), viewer, domain.NewItemID())
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Get(unknown) error = %v, want wrapped ErrItemNotFound", err)
	}
}

func TestItemService_ListInBin(t *testing.T) {
	repo := newFakeItemRepo()
	svc, _ := newTestItemService(repo)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")
	binID := domain.NewBinID()

	if _, err := svc.Create(context.Background(), "Stove", nil, 1, binID, actor); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Create(context.Background(), "Lantern", nil, 1, domain.NewBinID(), actor); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.ListInBin(context.Background(), viewer, binID)
	if err != nil {
		t.Fatalf("ListInBin: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Stove" {
		t.Errorf("ListInBin(%v) = %+v, want exactly the one item in that bin", binID, got)
	}
}

func TestItemService_ListVisible(t *testing.T) {
	repo := newFakeItemRepo()
	svc, _ := newTestItemService(repo)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")
	binID := domain.NewBinID()

	if _, err := svc.Create(context.Background(), "Stove", nil, 1, binID, actor); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Create(context.Background(), "Lantern", nil, 1, domain.NewBinID(), actor); err != nil {
		t.Fatalf("Create: %v", err)
	}

	t.Run("no filter returns every item", func(t *testing.T) {
		got, err := svc.ListVisible(context.Background(), viewer, domain.ItemFilter{})
		if err != nil {
			t.Fatalf("ListVisible: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("ListVisible(no filter) returned %d items, want 2", len(got))
		}
	})

	t.Run("bin filter narrows to that bin", func(t *testing.T) {
		got, err := svc.ListVisible(context.Background(), viewer, domain.ItemFilter{BinID: &binID})
		if err != nil {
			t.Fatalf("ListVisible: %v", err)
		}
		if len(got) != 1 || got[0].Name != "Stove" {
			t.Errorf("ListVisible(bin=%v) = %+v, want exactly the one item in that bin", binID, got)
		}
	})
}

func TestItemService_ListVisible_RepositoryErrorWrapped(t *testing.T) {
	repo := newFakeItemRepo()
	repo.listVisibleErr = errors.New("boom")
	svc, _ := newTestItemService(repo)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	if _, err := svc.ListVisible(context.Background(), viewer, domain.ItemFilter{}); err == nil {
		t.Error("ListVisible() error = nil, want the wrapped repository error")
	}
}

func TestItemService_Delete(t *testing.T) {
	repo := newFakeItemRepo()
	svc, _ := newTestItemService(repo)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

	it, err := svc.Create(context.Background(), "Stove", nil, 1, domain.NewBinID(), actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := svc.Delete(context.Background(), it.ID, actor); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := repo.items[it.ID]; ok {
		t.Error("Delete did not remove the item from the repository")
	}
}

// TestDeleteEmitsDeletedEvent proves NSTR-41's per-operation event contract
// for ItemService.Delete: exactly one EventDeleted row (in addition to
// Create's own EventCreated row), carrying the item's name as it was
// before the row was removed — item_event.item_id carries no foreign key
// specifically so this snapshot outlives the deleting transaction (see
// 00012_item_event.sql's own comment).
func TestDeleteEmitsDeletedEvent(t *testing.T) {
	repo := newFakeItemRepo()
	svc, events := newTestItemService(repo)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")
	it, err := svc.Create(context.Background(), "Stove", nil, 1, domain.NewBinID(), actor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := svc.Delete(context.Background(), it.ID, actor); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(events.events) != 2 {
		t.Fatalf("Delete appended events = %+v, want [created, deleted]", events.events)
	}
	got := events.events[1]
	if got.Kind != domain.EventDeleted {
		t.Errorf("event.Kind = %v, want %v", got.Kind, domain.EventDeleted)
	}
	if got.ItemName != "Stove" {
		t.Errorf("event.ItemName = %q, want %q (snapshot before the row was removed)", got.ItemName, "Stove")
	}
}

func TestItemService_Delete_NotFoundWrapped(t *testing.T) {
	repo := newFakeItemRepo()
	svc, _ := newTestItemService(repo)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Alice")

	err := svc.Delete(context.Background(), domain.NewItemID(), actor)
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Delete(unknown) error = %v, want wrapped ErrItemNotFound", err)
	}
}
