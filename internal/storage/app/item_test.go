package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// fakeItemRepo is an in-memory itemRepository fake for ItemService's
// hermetic unit tests. The *Err fields let a test simulate a repository
// failure for exactly the method under test, without needing a real
// database.
type fakeItemRepo struct {
	items map[domain.ItemID]*domain.Item

	createErr error
	getErr    error
	updateErr error
	listErr   error
	deleteErr error
}

func newFakeItemRepo() *fakeItemRepo {
	return &fakeItemRepo{items: make(map[domain.ItemID]*domain.Item)}
}

func (f *fakeItemRepo) Create(_ context.Context, it *domain.Item) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.items[it.ID] = it
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
	return it, nil
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

func TestNewItemService_PanicsOnNilDeps(t *testing.T) {
	tests := []struct {
		name  string
		build func() (repo *fakeItemRepo, logger *slog.Logger)
	}{
		{"nil repository", func() (*fakeItemRepo, *slog.Logger) { return nil, testLogger() }},
		{"nil logger", func() (*fakeItemRepo, *slog.Logger) { return newFakeItemRepo(), nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, logger := tt.build()
			defer func() {
				if recover() == nil {
					t.Error("NewItemService did not panic")
				}
			}()
			if repo == nil {
				app.NewItemService(nil, logger)
			} else {
				app.NewItemService(repo, logger)
			}
		})
	}
}

func TestItemService_Create(t *testing.T) {
	repo := newFakeItemRepo()
	svc := app.NewItemService(repo, testLogger())
	binID := domain.NewBinID()
	creator := identity.NewUserID()

	it, err := svc.Create(context.Background(), "Camping stove", nil, 1, binID, creator)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if it.CurrentBinID == nil || *it.CurrentBinID != binID {
		t.Errorf("Create: CurrentBinID = %v, want %v", it.CurrentBinID, binID)
	}
	if it.CreatedBy != creator {
		t.Errorf("Create: CreatedBy = %v, want %v", it.CreatedBy, creator)
	}
	if _, ok := repo.items[it.ID]; !ok {
		t.Error("Create did not persist the item via the repository")
	}
}

func TestItemService_Create_ValidationRejected(t *testing.T) {
	repo := newFakeItemRepo()
	svc := app.NewItemService(repo, testLogger())
	binID := domain.NewBinID()
	creator := identity.NewUserID()

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
			_, err := svc.Create(context.Background(), tt.itemName, nil, tt.quantity, binID, creator)
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
	svc := app.NewItemService(repo, testLogger())

	_, err := svc.Create(context.Background(), "Stove", nil, 1, domain.NewBinID(), identity.NewUserID())
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("Create() error = %v, want wrapped ErrBinNotFound", err)
	}
}

func TestItemService_Edit(t *testing.T) {
	repo := newFakeItemRepo()
	svc := app.NewItemService(repo, testLogger())
	binID := domain.NewBinID()
	it, err := svc.Create(context.Background(), "Stove", nil, 1, binID, identity.NewUserID())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	desc := "Two-burner camping stove"
	if err := svc.Edit(context.Background(), it.ID, "Camping stove", &desc, 2); err != nil {
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

func TestItemService_Edit_ValidationRejected(t *testing.T) {
	repo := newFakeItemRepo()
	svc := app.NewItemService(repo, testLogger())

	if err := svc.Edit(context.Background(), domain.NewItemID(), "", nil, 1); !errors.Is(err, domain.ErrItemNameRequired) {
		t.Errorf("Edit(blank name) error = %v, want ErrItemNameRequired", err)
	}
	if err := svc.Edit(context.Background(), domain.NewItemID(), "Stove", nil, 0); !errors.Is(err, domain.ErrInvalidQuantity) {
		t.Errorf("Edit(zero quantity) error = %v, want ErrInvalidQuantity", err)
	}
}

func TestItemService_Edit_NotFoundWrapped(t *testing.T) {
	repo := newFakeItemRepo()
	svc := app.NewItemService(repo, testLogger())

	err := svc.Edit(context.Background(), domain.NewItemID(), "Stove", nil, 1)
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Edit(unknown) error = %v, want wrapped ErrItemNotFound", err)
	}
}

func TestItemService_Get(t *testing.T) {
	repo := newFakeItemRepo()
	svc := app.NewItemService(repo, testLogger())
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	it, err := svc.Create(context.Background(), "Stove", nil, 1, domain.NewBinID(), identity.NewUserID())
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
	svc := app.NewItemService(repo, testLogger())
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	_, err := svc.Get(context.Background(), viewer, domain.NewItemID())
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Get(unknown) error = %v, want wrapped ErrItemNotFound", err)
	}
}

func TestItemService_ListInBin(t *testing.T) {
	repo := newFakeItemRepo()
	svc := app.NewItemService(repo, testLogger())
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")
	binID := domain.NewBinID()

	if _, err := svc.Create(context.Background(), "Stove", nil, 1, binID, identity.NewUserID()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Create(context.Background(), "Lantern", nil, 1, domain.NewBinID(), identity.NewUserID()); err != nil {
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

func TestItemService_Delete(t *testing.T) {
	repo := newFakeItemRepo()
	svc := app.NewItemService(repo, testLogger())

	it, err := svc.Create(context.Background(), "Stove", nil, 1, domain.NewBinID(), identity.NewUserID())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := svc.Delete(context.Background(), it.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := repo.items[it.ID]; ok {
		t.Error("Delete did not remove the item from the repository")
	}
}

func TestItemService_Delete_NotFoundWrapped(t *testing.T) {
	repo := newFakeItemRepo()
	svc := app.NewItemService(repo, testLogger())

	err := svc.Delete(context.Background(), domain.NewItemID())
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Delete(unknown) error = %v, want wrapped ErrItemNotFound", err)
	}
}
