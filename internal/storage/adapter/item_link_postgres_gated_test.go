package adapter_test

import (
	"errors"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/adapter"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// Every test below reuses newItemFixture/newItem from
// item_postgres_gated_test.go (same package, same "storage" derived
// database — dbtest.Harness.NewIsolatedPool must be called exactly once per
// test) rather than standing up a second, parallel fixture type just to add
// one more repository to it.

func TestNewItemLinkRepository_NilExecutorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewItemLinkRepository(nil) did not panic")
		}
	}()
	adapter.NewItemLinkRepository(nil)
}

func TestItemLinkRepository_CreateAndListByItem(t *testing.T) {
	f := newItemFixture(t)
	linkRepo := adapter.NewItemLinkRepository(f.pool)
	creator := f.seedUser(t, identity.RoleAdult)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem(f.household, "Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create(item): %v", err)
	}

	l := &domain.ItemLink{ID: domain.NewItemLinkID(), ItemID: it.ID, Label: "Manual", URL: "https://example.com/manual", Position: 0}
	if err := linkRepo.Create(testCtx(t), f.household, l); err != nil {
		t.Fatalf("Create(link): %v", err)
	}
	if l.CreatedAt.IsZero() || l.UpdatedAt.IsZero() {
		t.Error("Create left a timestamp zero")
	}

	got, err := linkRepo.ListByItem(testCtx(t), f.household, it.ID)
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(got) != 1 || got[0].Label != "Manual" || got[0].URL != "https://example.com/manual" {
		t.Errorf("ListByItem = %+v, want exactly the created link", got)
	}
}

func TestItemLinkRepository_ListByItem_OrderedByPosition(t *testing.T) {
	f := newItemFixture(t)
	linkRepo := adapter.NewItemLinkRepository(f.pool)
	creator := f.seedUser(t, identity.RoleAdult)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem(f.household, "Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create(item): %v", err)
	}

	second := &domain.ItemLink{ID: domain.NewItemLinkID(), ItemID: it.ID, Label: "Second", URL: "https://example.com/2", Position: 1}
	first := &domain.ItemLink{ID: domain.NewItemLinkID(), ItemID: it.ID, Label: "First", URL: "https://example.com/1", Position: 0}
	if err := linkRepo.Create(testCtx(t), f.household, second); err != nil {
		t.Fatalf("Create(second): %v", err)
	}
	if err := linkRepo.Create(testCtx(t), f.household, first); err != nil {
		t.Fatalf("Create(first): %v", err)
	}

	got, err := linkRepo.ListByItem(testCtx(t), f.household, it.ID)
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(got) != 2 || got[0].Label != "First" || got[1].Label != "Second" {
		t.Errorf("ListByItem order = %+v, want [First, Second] by position", got)
	}
}

// TestItemLinkRepository_ListByItem_TieBreaksByID proves two links sharing a
// position (the migration deliberately allows this, see its own comment)
// still resolve to a deterministic order.
func TestItemLinkRepository_ListByItem_TieBreaksByID(t *testing.T) {
	f := newItemFixture(t)
	linkRepo := adapter.NewItemLinkRepository(f.pool)
	creator := f.seedUser(t, identity.RoleAdult)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem(f.household, "Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create(item): %v", err)
	}

	a := &domain.ItemLink{ID: domain.NewItemLinkID(), ItemID: it.ID, Label: "A", URL: "https://example.com/a", Position: 0}
	b := &domain.ItemLink{ID: domain.NewItemLinkID(), ItemID: it.ID, Label: "B", URL: "https://example.com/b", Position: 0}
	if err := linkRepo.Create(testCtx(t), f.household, a); err != nil {
		t.Fatalf("Create(a): %v", err)
	}
	if err := linkRepo.Create(testCtx(t), f.household, b); err != nil {
		t.Fatalf("Create(b): %v", err)
	}

	got, err := linkRepo.ListByItem(testCtx(t), f.household, it.ID)
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByItem returned %d links, want 2", len(got))
	}
	wantFirst, wantSecond := a, b
	if b.ID.String() < a.ID.String() {
		wantFirst, wantSecond = b, a
	}
	if got[0].ID != wantFirst.ID || got[1].ID != wantSecond.ID {
		t.Errorf("ListByItem tie-break order = %+v, want id-ascending among equal positions", got)
	}
}

func TestItemLinkRepository_ListByItem_Empty(t *testing.T) {
	f := newItemFixture(t)
	linkRepo := adapter.NewItemLinkRepository(f.pool)
	got, err := linkRepo.ListByItem(testCtx(t), f.household, domain.NewItemID())
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByItem(no links) = %+v, want empty slice", got)
	}
}

func TestItemLinkRepository_Create_UnknownItemRejected(t *testing.T) {
	f := newItemFixture(t)
	linkRepo := adapter.NewItemLinkRepository(f.pool)
	l := &domain.ItemLink{ID: domain.NewItemLinkID(), ItemID: domain.NewItemID(), Label: "Ghost", URL: "https://example.com", Position: 0}

	err := linkRepo.Create(testCtx(t), f.household, l)
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Create(unknown item) = %v, want ErrItemNotFound", err)
	}
}

func TestItemLinkRepository_NextPosition(t *testing.T) {
	f := newItemFixture(t)
	linkRepo := adapter.NewItemLinkRepository(f.pool)
	creator := f.seedUser(t, identity.RoleAdult)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem(f.household, "Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create(item): %v", err)
	}

	next, err := linkRepo.NextPosition(testCtx(t), f.household, it.ID)
	if err != nil {
		t.Fatalf("NextPosition(empty): %v", err)
	}
	if next != 0 {
		t.Errorf("NextPosition(empty) = %d, want 0", next)
	}

	l := &domain.ItemLink{ID: domain.NewItemLinkID(), ItemID: it.ID, Label: "Manual", URL: "https://example.com", Position: next}
	if err := linkRepo.Create(testCtx(t), f.household, l); err != nil {
		t.Fatalf("Create: %v", err)
	}

	next, err = linkRepo.NextPosition(testCtx(t), f.household, it.ID)
	if err != nil {
		t.Fatalf("NextPosition(populated): %v", err)
	}
	if next != 1 {
		t.Errorf("NextPosition(populated) = %d, want 1", next)
	}
}

func TestItemLinkRepository_Update(t *testing.T) {
	f := newItemFixture(t)
	linkRepo := adapter.NewItemLinkRepository(f.pool)
	creator := f.seedUser(t, identity.RoleAdult)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem(f.household, "Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create(item): %v", err)
	}
	l := &domain.ItemLink{ID: domain.NewItemLinkID(), ItemID: it.ID, Label: "Manual", URL: "https://example.com/old", Position: 0}
	if err := linkRepo.Create(testCtx(t), f.household, l); err != nil {
		t.Fatalf("Create(link): %v", err)
	}

	if err := linkRepo.Update(testCtx(t), f.household, it.ID, l.ID, "Owner's manual", "https://example.com/new"); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := linkRepo.ListByItem(testCtx(t), f.household, it.ID)
	if err != nil {
		t.Fatalf("ListByItem after Update: %v", err)
	}
	if len(got) != 1 || got[0].Label != "Owner's manual" || got[0].URL != "https://example.com/new" {
		t.Errorf("ListByItem after Update = %+v, want the updated label/url", got)
	}
}

func TestItemLinkRepository_Update_NotFound(t *testing.T) {
	f := newItemFixture(t)
	linkRepo := adapter.NewItemLinkRepository(f.pool)
	err := linkRepo.Update(testCtx(t), f.household, domain.NewItemID(), domain.NewItemLinkID(), "Manual", "https://example.com")
	if !errors.Is(err, domain.ErrItemLinkNotFound) {
		t.Errorf("Update(unknown) = %v, want ErrItemLinkNotFound", err)
	}
}

// TestItemLinkRepository_Update_WrongItemRejected proves a link id from
// another item 404s rather than being mutated — the item-scoping contract
// domain.ItemLinkRepository's own doc requires.
func TestItemLinkRepository_Update_WrongItemRejected(t *testing.T) {
	f := newItemFixture(t)
	linkRepo := adapter.NewItemLinkRepository(f.pool)
	creator := f.seedUser(t, identity.RoleAdult)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	itA := newItem(f.household, "Stove", bin, creator)
	itB := newItem(f.household, "Lantern", bin, creator)
	if err := f.repo.Create(testCtx(t), itA); err != nil {
		t.Fatalf("Create(itemA): %v", err)
	}
	if err := f.repo.Create(testCtx(t), itB); err != nil {
		t.Fatalf("Create(itemB): %v", err)
	}
	l := &domain.ItemLink{ID: domain.NewItemLinkID(), ItemID: itA.ID, Label: "Manual", URL: "https://example.com", Position: 0}
	if err := linkRepo.Create(testCtx(t), f.household, l); err != nil {
		t.Fatalf("Create(link): %v", err)
	}

	err := linkRepo.Update(testCtx(t), f.household, itB.ID, l.ID, "Hijacked", "https://evil.example.com")
	if !errors.Is(err, domain.ErrItemLinkNotFound) {
		t.Errorf("Update(link, wrong item) = %v, want ErrItemLinkNotFound", err)
	}
}

func TestItemLinkRepository_Delete(t *testing.T) {
	f := newItemFixture(t)
	linkRepo := adapter.NewItemLinkRepository(f.pool)
	creator := f.seedUser(t, identity.RoleAdult)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem(f.household, "Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create(item): %v", err)
	}
	l := &domain.ItemLink{ID: domain.NewItemLinkID(), ItemID: it.ID, Label: "Manual", URL: "https://example.com", Position: 0}
	if err := linkRepo.Create(testCtx(t), f.household, l); err != nil {
		t.Fatalf("Create(link): %v", err)
	}

	if err := linkRepo.Delete(testCtx(t), f.household, it.ID, l.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := linkRepo.ListByItem(testCtx(t), f.household, it.ID)
	if err != nil {
		t.Fatalf("ListByItem after Delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByItem after Delete = %+v, want empty", got)
	}
}

func TestItemLinkRepository_Delete_NotFound(t *testing.T) {
	f := newItemFixture(t)
	linkRepo := adapter.NewItemLinkRepository(f.pool)
	err := linkRepo.Delete(testCtx(t), f.household, domain.NewItemID(), domain.NewItemLinkID())
	if !errors.Is(err, domain.ErrItemLinkNotFound) {
		t.Errorf("Delete(unknown) = %v, want ErrItemLinkNotFound", err)
	}
}

// TestItemLinkRepository_Delete_WrongItemRejected mirrors Update's own
// cross-item guard.
func TestItemLinkRepository_Delete_WrongItemRejected(t *testing.T) {
	f := newItemFixture(t)
	linkRepo := adapter.NewItemLinkRepository(f.pool)
	creator := f.seedUser(t, identity.RoleAdult)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	itA := newItem(f.household, "Stove", bin, creator)
	itB := newItem(f.household, "Lantern", bin, creator)
	if err := f.repo.Create(testCtx(t), itA); err != nil {
		t.Fatalf("Create(itemA): %v", err)
	}
	if err := f.repo.Create(testCtx(t), itB); err != nil {
		t.Fatalf("Create(itemB): %v", err)
	}
	l := &domain.ItemLink{ID: domain.NewItemLinkID(), ItemID: itA.ID, Label: "Manual", URL: "https://example.com", Position: 0}
	if err := linkRepo.Create(testCtx(t), f.household, l); err != nil {
		t.Fatalf("Create(link): %v", err)
	}

	err := linkRepo.Delete(testCtx(t), f.household, itB.ID, l.ID)
	if !errors.Is(err, domain.ErrItemLinkNotFound) {
		t.Errorf("Delete(link, wrong item) = %v, want ErrItemLinkNotFound", err)
	}
	got, err := linkRepo.ListByItem(testCtx(t), f.household, itA.ID)
	if err != nil {
		t.Fatalf("ListByItem after rejected delete: %v", err)
	}
	if len(got) != 1 {
		t.Error("Delete(wrong item) must leave the link row in place")
	}
}

// TestItemLinkRepository_CascadeDeleteWithItem proves item_id's ON DELETE
// CASCADE: removing the owning item removes its links with it, with no
// separate cleanup step.
func TestItemLinkRepository_CascadeDeleteWithItem(t *testing.T) {
	f := newItemFixture(t)
	linkRepo := adapter.NewItemLinkRepository(f.pool)
	creator := f.seedUser(t, identity.RoleAdult)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem(f.household, "Stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create(item): %v", err)
	}
	l := &domain.ItemLink{ID: domain.NewItemLinkID(), ItemID: it.ID, Label: "Manual", URL: "https://example.com", Position: 0}
	if err := linkRepo.Create(testCtx(t), f.household, l); err != nil {
		t.Fatalf("Create(link): %v", err)
	}

	if err := f.repo.Delete(testCtx(t), f.household, it.ID); err != nil {
		t.Fatalf("Delete(item): %v", err)
	}

	got, err := linkRepo.ListByItem(testCtx(t), f.household, it.ID)
	if err != nil {
		t.Fatalf("ListByItem after item cascade delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByItem after item delete = %+v, want empty (cascade)", got)
	}
}
