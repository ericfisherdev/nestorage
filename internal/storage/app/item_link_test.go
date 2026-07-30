package app_test

import (
	"context"
	"errors"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// fakeItemLinkRepo is an in-memory itemLinkRepository fake for
// ItemLinkService's hermetic unit tests, mirroring fakeItemRepo's own shape.
type fakeItemLinkRepo struct {
	links map[domain.ItemLinkID]*domain.ItemLink

	createErr       error
	updateErr       error
	deleteErr       error
	listErr         error
	nextPositionErr error
}

func newFakeItemLinkRepo() *fakeItemLinkRepo {
	return &fakeItemLinkRepo{links: make(map[domain.ItemLinkID]*domain.ItemLink)}
}

func (f *fakeItemLinkRepo) Create(_ context.Context, l *domain.ItemLink) error {
	if f.createErr != nil {
		return f.createErr
	}
	cp := *l
	f.links[l.ID] = &cp
	return nil
}

func (f *fakeItemLinkRepo) Update(_ context.Context, itemID domain.ItemID, id domain.ItemLinkID, label, rawURL string) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	existing, ok := f.links[id]
	if !ok || existing.ItemID != itemID {
		return domain.ErrItemLinkNotFound
	}
	existing.Label, existing.URL = label, rawURL
	return nil
}

func (f *fakeItemLinkRepo) Delete(_ context.Context, itemID domain.ItemID, id domain.ItemLinkID) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	existing, ok := f.links[id]
	if !ok || existing.ItemID != itemID {
		return domain.ErrItemLinkNotFound
	}
	delete(f.links, id)
	return nil
}

func (f *fakeItemLinkRepo) ListByItem(_ context.Context, itemID domain.ItemID) ([]domain.ItemLink, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	links := make([]domain.ItemLink, 0)
	for _, l := range f.links {
		if l.ItemID == itemID {
			links = append(links, *l)
		}
	}
	return links, nil
}

func (f *fakeItemLinkRepo) NextPosition(_ context.Context, itemID domain.ItemID) (int, error) {
	if f.nextPositionErr != nil {
		return 0, f.nextPositionErr
	}
	count := 0
	for _, l := range f.links {
		if l.ItemID == itemID {
			count++
		}
	}
	return count, nil
}

// fakeItemLinkItemGetter is a configurable itemLinkItemGetter fake standing
// in for the visibility-scoped item lookup every ItemLinkService method
// runs first — a nil visibleErr means the item exists and is visible.
type fakeItemLinkItemGetter struct {
	visibleErr error
	getCalls   int
}

func (f *fakeItemLinkItemGetter) Get(_ context.Context, _ identity.Principal, _ domain.ItemID) (*domain.Item, error) {
	f.getCalls++
	if f.visibleErr != nil {
		return nil, f.visibleErr
	}
	return &domain.Item{}, nil
}

func TestNewItemLinkService_PanicsOnNilDeps(t *testing.T) {
	t.Run("nil repository", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewItemLinkService did not panic")
			}
		}()
		app.NewItemLinkService(nil, &fakeItemLinkItemGetter{}, testLogger())
	})
	t.Run("nil item getter", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewItemLinkService did not panic")
			}
		}()
		app.NewItemLinkService(newFakeItemLinkRepo(), nil, testLogger())
	})
	t.Run("nil logger", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewItemLinkService did not panic")
			}
		}()
		app.NewItemLinkService(newFakeItemLinkRepo(), &fakeItemLinkItemGetter{}, nil)
	})
}

func TestItemLinkService_Add(t *testing.T) {
	repo := newFakeItemLinkRepo()
	items := &fakeItemLinkItemGetter{}
	svc := app.NewItemLinkService(repo, items, testLogger())
	itemID := domain.NewItemID()
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Viewer")

	l, err := svc.Add(context.Background(), viewer, itemID, "Manual", "https://example.com/manual.pdf")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if l.ItemID != itemID || l.Label != "Manual" || l.URL != "https://example.com/manual.pdf" {
		t.Errorf("Add() = %+v, want it to match the input", l)
	}
	if l.Position != 0 {
		t.Errorf("Add() first link Position = %d, want 0", l.Position)
	}
	if items.getCalls != 1 {
		t.Errorf("Add must authorize via the item getter exactly once, got %d calls", items.getCalls)
	}
}

func TestItemLinkService_Add_AssignsNextPosition(t *testing.T) {
	repo := newFakeItemLinkRepo()
	svc := app.NewItemLinkService(repo, &fakeItemLinkItemGetter{}, testLogger())
	itemID := domain.NewItemID()
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Viewer")

	first, err := svc.Add(context.Background(), viewer, itemID, "Manual", "https://example.com/manual")
	if err != nil {
		t.Fatalf("Add(first): %v", err)
	}
	second, err := svc.Add(context.Background(), viewer, itemID, "Warranty", "https://example.com/warranty")
	if err != nil {
		t.Fatalf("Add(second): %v", err)
	}
	if first.Position != 0 || second.Position != 1 {
		t.Errorf("positions = [%d, %d], want [0, 1]", first.Position, second.Position)
	}
}

func TestItemLinkService_Add_ValidationRejected(t *testing.T) {
	repo := newFakeItemLinkRepo()
	items := &fakeItemLinkItemGetter{}
	svc := app.NewItemLinkService(repo, items, testLogger())
	itemID := domain.NewItemID()
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Viewer")

	tests := []struct {
		name    string
		label   string
		url     string
		wantErr error
	}{
		{"blank label", "  ", "https://example.com", domain.ErrItemLinkLabelRequired},
		{"javascript url", "Manual", "javascript:alert(1)", domain.ErrInvalidItemLinkURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Add(context.Background(), viewer, itemID, tt.label, tt.url)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Add(%q, %q) error = %v, want %v", tt.label, tt.url, err, tt.wantErr)
			}
		})
	}
	if len(repo.links) != 0 {
		t.Error("Add must not reach the repository when validation fails")
	}
	if items.getCalls != 0 {
		t.Error("Add must validate before ever checking item visibility")
	}
}

func TestItemLinkService_Add_ItemNotVisibleWrapped(t *testing.T) {
	repo := newFakeItemLinkRepo()
	items := &fakeItemLinkItemGetter{visibleErr: domain.ErrItemNotFound}
	svc := app.NewItemLinkService(repo, items, testLogger())

	_, err := svc.Add(context.Background(), identity.Principal{}, domain.NewItemID(), "Manual", "https://example.com")
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Add(invisible item) error = %v, want wrapped ErrItemNotFound", err)
	}
	if len(repo.links) != 0 {
		t.Error("Add must not persist a link when the item is not visible")
	}
}

func TestItemLinkService_Edit(t *testing.T) {
	repo := newFakeItemLinkRepo()
	svc := app.NewItemLinkService(repo, &fakeItemLinkItemGetter{}, testLogger())
	itemID := domain.NewItemID()
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Viewer")

	l, err := svc.Add(context.Background(), viewer, itemID, "Manual", "https://example.com/old")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := svc.Edit(context.Background(), viewer, itemID, l.ID, "Owner's manual", "https://example.com/new"); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	got := repo.links[l.ID]
	if got.Label != "Owner's manual" || got.URL != "https://example.com/new" {
		t.Errorf("Edit did not update the link: %+v", got)
	}
}

func TestItemLinkService_Edit_ValidationRejected(t *testing.T) {
	svc := app.NewItemLinkService(newFakeItemLinkRepo(), &fakeItemLinkItemGetter{}, testLogger())
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Viewer")

	err := svc.Edit(context.Background(), viewer, domain.NewItemID(), domain.NewItemLinkID(), "", "https://example.com")
	if !errors.Is(err, domain.ErrItemLinkLabelRequired) {
		t.Errorf("Edit(blank label) error = %v, want ErrItemLinkLabelRequired", err)
	}
}

func TestItemLinkService_Edit_NotFoundWrapped(t *testing.T) {
	svc := app.NewItemLinkService(newFakeItemLinkRepo(), &fakeItemLinkItemGetter{}, testLogger())
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Viewer")

	err := svc.Edit(context.Background(), viewer, domain.NewItemID(), domain.NewItemLinkID(), "Manual", "https://example.com")
	if !errors.Is(err, domain.ErrItemLinkNotFound) {
		t.Errorf("Edit(unknown link) error = %v, want wrapped ErrItemLinkNotFound", err)
	}
}

func TestItemLinkService_Edit_ItemNotVisibleWrapped(t *testing.T) {
	items := &fakeItemLinkItemGetter{visibleErr: domain.ErrItemNotFound}
	svc := app.NewItemLinkService(newFakeItemLinkRepo(), items, testLogger())

	err := svc.Edit(context.Background(), identity.Principal{}, domain.NewItemID(), domain.NewItemLinkID(), "Manual", "https://example.com")
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Edit(invisible item) error = %v, want wrapped ErrItemNotFound", err)
	}
}

func TestItemLinkService_Remove(t *testing.T) {
	repo := newFakeItemLinkRepo()
	svc := app.NewItemLinkService(repo, &fakeItemLinkItemGetter{}, testLogger())
	itemID := domain.NewItemID()
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Viewer")

	l, err := svc.Add(context.Background(), viewer, itemID, "Manual", "https://example.com")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.Remove(context.Background(), viewer, itemID, l.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := repo.links[l.ID]; ok {
		t.Error("Remove did not delete the link from the repository")
	}
}

func TestItemLinkService_Remove_ItemNotVisibleWrapped(t *testing.T) {
	items := &fakeItemLinkItemGetter{visibleErr: domain.ErrItemNotFound}
	svc := app.NewItemLinkService(newFakeItemLinkRepo(), items, testLogger())

	err := svc.Remove(context.Background(), identity.Principal{}, domain.NewItemID(), domain.NewItemLinkID())
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Remove(invisible item) error = %v, want wrapped ErrItemNotFound", err)
	}
}

func TestItemLinkService_Remove_NotFoundWrapped(t *testing.T) {
	svc := app.NewItemLinkService(newFakeItemLinkRepo(), &fakeItemLinkItemGetter{}, testLogger())
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Viewer")

	err := svc.Remove(context.Background(), viewer, domain.NewItemID(), domain.NewItemLinkID())
	if !errors.Is(err, domain.ErrItemLinkNotFound) {
		t.Errorf("Remove(unknown link) error = %v, want wrapped ErrItemLinkNotFound", err)
	}
}

func TestItemLinkService_ListForItem(t *testing.T) {
	repo := newFakeItemLinkRepo()
	svc := app.NewItemLinkService(repo, &fakeItemLinkItemGetter{}, testLogger())
	itemID := domain.NewItemID()
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Viewer")

	if _, err := svc.Add(context.Background(), viewer, itemID, "Manual", "https://example.com"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := svc.Add(context.Background(), viewer, domain.NewItemID(), "Other item's link", "https://example.com/other"); err != nil {
		t.Fatalf("Add(other item): %v", err)
	}

	got, err := svc.ListForItem(context.Background(), viewer, itemID)
	if err != nil {
		t.Fatalf("ListForItem: %v", err)
	}
	if len(got) != 1 || got[0].Label != "Manual" {
		t.Errorf("ListForItem(%v) = %+v, want exactly the one link on that item", itemID, got)
	}
}

func TestItemLinkService_ListForItem_ItemNotVisibleWrapped(t *testing.T) {
	items := &fakeItemLinkItemGetter{visibleErr: domain.ErrItemNotFound}
	svc := app.NewItemLinkService(newFakeItemLinkRepo(), items, testLogger())

	_, err := svc.ListForItem(context.Background(), identity.Principal{}, domain.NewItemID())
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("ListForItem(invisible item) error = %v, want wrapped ErrItemNotFound", err)
	}
}

func TestItemLinkService_ListForItem_Empty(t *testing.T) {
	svc := app.NewItemLinkService(newFakeItemLinkRepo(), &fakeItemLinkItemGetter{}, testLogger())
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Viewer")

	got, err := svc.ListForItem(context.Background(), viewer, domain.NewItemID())
	if err != nil {
		t.Fatalf("ListForItem: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListForItem(no links) = %+v, want empty slice", got)
	}
}
