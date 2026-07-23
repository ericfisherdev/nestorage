package app_test

import (
	"context"
	"errors"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// fakeBinReadWriter is an in-memory binReadWriter fake for BinService's
// hermetic unit tests, mirroring fakeItemRepo's shape.
type fakeBinReadWriter struct {
	bins map[domain.BinID]*domain.Bin

	createErr     error
	findByIDErr   error
	findByCodeErr error
	listErr       error
	listByLocErr  error
	updateErr     error
	deleteErr     error
}

func newFakeBinReadWriter() *fakeBinReadWriter {
	return &fakeBinReadWriter{bins: make(map[domain.BinID]*domain.Bin)}
}

func (f *fakeBinReadWriter) Create(_ context.Context, b *domain.Bin) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.bins[b.ID] = b
	return nil
}

func (f *fakeBinReadWriter) FindVisibleByID(_ context.Context, _ identity.Principal, id domain.BinID) (*domain.Bin, error) {
	if f.findByIDErr != nil {
		return nil, f.findByIDErr
	}
	b, ok := f.bins[id]
	if !ok {
		return nil, domain.ErrBinNotFound
	}
	return b, nil
}

func (f *fakeBinReadWriter) FindVisibleByCode(_ context.Context, _ identity.Principal, code string) (*domain.Bin, error) {
	if f.findByCodeErr != nil {
		return nil, f.findByCodeErr
	}
	for _, b := range f.bins {
		if b.Code == domain.NormalizeBinCode(code) {
			return b, nil
		}
	}
	return nil, domain.ErrBinNotFound
}

func (f *fakeBinReadWriter) ListVisible(_ context.Context, _ identity.Principal) ([]domain.Bin, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	bins := make([]domain.Bin, 0, len(f.bins))
	for _, b := range f.bins {
		bins = append(bins, *b)
	}
	return bins, nil
}

func (f *fakeBinReadWriter) ListVisibleByLocation(_ context.Context, _ identity.Principal, locationID domain.LocationID) ([]domain.Bin, error) {
	if f.listByLocErr != nil {
		return nil, f.listByLocErr
	}
	bins := make([]domain.Bin, 0)
	for _, b := range f.bins {
		if b.LocationID == locationID {
			bins = append(bins, *b)
		}
	}
	return bins, nil
}

func (f *fakeBinReadWriter) Update(_ context.Context, _ identity.Principal, b *domain.Bin) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	existing, ok := f.bins[b.ID]
	if !ok {
		return domain.ErrBinNotFound
	}
	existing.Name, existing.Description, existing.OwnerID, existing.Visibility = b.Name, b.Description, b.OwnerID, b.Visibility
	return nil
}

func (f *fakeBinReadWriter) Delete(_ context.Context, _ identity.Principal, id domain.BinID) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.bins[id]; !ok {
		return domain.ErrBinNotFound
	}
	delete(f.bins, id)
	return nil
}

// fakeMemberDirectory is an in-memory memberLister fake for BinService's
// owner-enrichment tests.
type fakeMemberDirectory struct {
	members []identity.User
	err     error
}

func (f *fakeMemberDirectory) List(_ context.Context) ([]identity.User, error) {
	return f.members, f.err
}

// fakeItemCounter is an in-memory itemCounter fake for BinService's
// item-count enrichment tests.
type fakeItemCounter struct {
	counts map[domain.BinID]int
	err    error
}

func (f *fakeItemCounter) CountsByBin(_ context.Context, _ identity.Principal) (map[domain.BinID]int, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.counts, nil
}

func newBinService(bins *fakeBinReadWriter, members *fakeMemberDirectory, items *fakeItemCounter) *app.BinService {
	if members == nil {
		members = &fakeMemberDirectory{}
	}
	if items == nil {
		items = &fakeItemCounter{}
	}
	return app.NewBinService(bins, members, items, testLogger())
}

func TestNewBinService_PanicsOnNilDeps(t *testing.T) {
	t.Run("nil binReadWriter", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewBinService did not panic")
			}
		}()
		app.NewBinService(nil, &fakeMemberDirectory{}, &fakeItemCounter{}, testLogger())
	})
	t.Run("nil memberLister", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewBinService did not panic")
			}
		}()
		app.NewBinService(newFakeBinReadWriter(), nil, &fakeItemCounter{}, testLogger())
	})
	t.Run("nil itemCounter", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewBinService did not panic")
			}
		}()
		app.NewBinService(newFakeBinReadWriter(), &fakeMemberDirectory{}, nil, testLogger())
	})
	t.Run("nil logger", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewBinService did not panic")
			}
		}()
		app.NewBinService(newFakeBinReadWriter(), &fakeMemberDirectory{}, &fakeItemCounter{}, nil)
	})
}

func TestBinService_ListVisible_EnrichesOwnerAndCount(t *testing.T) {
	bins := newFakeBinReadWriter()
	owner := identity.User{ID: identity.NewUserID(), DisplayName: "Maya", Color: identity.ColorIndigo}
	b := &domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Winter Clothes", OwnerID: &owner.ID}
	if err := bins.Create(context.Background(), b); err != nil {
		t.Fatalf("seed bin: %v", err)
	}

	members := &fakeMemberDirectory{members: []identity.User{owner}}
	items := &fakeItemCounter{counts: map[domain.BinID]int{b.ID: 24}}
	svc := newBinService(bins, members, items)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	got, err := svc.ListVisible(context.Background(), viewer)
	if err != nil {
		t.Fatalf("ListVisible: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListVisible() = %d views, want 1", len(got))
	}
	view := got[0]
	if view.Owner == nil || view.Owner.Name != "Maya" || view.Owner.Initials != "M" || view.Owner.Color != identity.ColorIndigo {
		t.Errorf("ListVisible() Owner = %+v, want Maya/M/indigo", view.Owner)
	}
	if view.ItemCount != 24 {
		t.Errorf("ListVisible() ItemCount = %d, want 24", view.ItemCount)
	}
}

func TestBinService_ListVisible_UnknownOwnerYieldsNilOwner(t *testing.T) {
	bins := newFakeBinReadWriter()
	deletedOwner := identity.NewUserID()
	b := &domain.Bin{ID: domain.NewBinID(), Code: "A2", Name: "Orphaned", OwnerID: &deletedOwner}
	if err := bins.Create(context.Background(), b); err != nil {
		t.Fatalf("seed bin: %v", err)
	}

	svc := newBinService(bins, &fakeMemberDirectory{}, &fakeItemCounter{})
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	got, err := svc.ListVisible(context.Background(), viewer)
	if err != nil {
		t.Fatalf("ListVisible: %v", err)
	}
	if got[0].Owner != nil {
		t.Errorf("ListVisible() Owner = %+v, want nil for an owner no longer in the directory", got[0].Owner)
	}
}

func TestBinService_ListVisible_OwnerBlankNameYieldsQuestionMarkInitials(t *testing.T) {
	bins := newFakeBinReadWriter()
	owner := identity.User{ID: identity.NewUserID(), DisplayName: "   ", Color: identity.ColorIndigo}
	b := &domain.Bin{ID: domain.NewBinID(), Code: "A1", OwnerID: &owner.ID}
	if err := bins.Create(context.Background(), b); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
	members := &fakeMemberDirectory{members: []identity.User{owner}}
	svc := newBinService(bins, members, nil)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	got, err := svc.ListVisible(context.Background(), viewer)
	if err != nil {
		t.Fatalf("ListVisible: %v", err)
	}
	if got[0].Owner == nil || got[0].Owner.Initials != "?" {
		t.Errorf("ListVisible() Owner.Initials = %+v, want \"?\" for a blank display name", got[0].Owner)
	}
}

func TestBinService_ListVisibleByLocation(t *testing.T) {
	bins := newFakeBinReadWriter()
	garage := domain.NewLocationID()
	attic := domain.NewLocationID()
	if err := bins.Create(context.Background(), &domain.Bin{ID: domain.NewBinID(), Code: "G1", LocationID: garage}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := bins.Create(context.Background(), &domain.Bin{ID: domain.NewBinID(), Code: "A1", LocationID: attic}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	svc := newBinService(bins, nil, nil)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	got, err := svc.ListVisibleByLocation(context.Background(), viewer, garage)
	if err != nil {
		t.Fatalf("ListVisibleByLocation: %v", err)
	}
	if len(got) != 1 || got[0].Bin.Code != "G1" {
		t.Errorf("ListVisibleByLocation(garage) = %+v, want exactly the garage bin", got)
	}
}

func TestBinService_ListVisible_RepositoryErrorWrapped(t *testing.T) {
	bins := newFakeBinReadWriter()
	bins.listErr = errors.New("boom")
	svc := newBinService(bins, nil, nil)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	_, err := svc.ListVisible(context.Background(), viewer)
	if err == nil {
		t.Fatal("ListVisible() error = nil, want a wrapped error")
	}
}

func TestBinService_ListVisible_MemberListErrorWrapped(t *testing.T) {
	bins := newFakeBinReadWriter()
	members := &fakeMemberDirectory{err: errors.New("directory down")}
	svc := newBinService(bins, members, nil)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	_, err := svc.ListVisible(context.Background(), viewer)
	if err == nil {
		t.Fatal("ListVisible() error = nil, want a wrapped error when the member directory fails")
	}
}

func TestBinService_ListVisible_ItemCounterErrorWrapped(t *testing.T) {
	bins := newFakeBinReadWriter()
	items := &fakeItemCounter{err: errors.New("counts down")}
	svc := newBinService(bins, nil, items)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	_, err := svc.ListVisible(context.Background(), viewer)
	if err == nil {
		t.Fatal("ListVisible() error = nil, want a wrapped error when the item counter fails")
	}
}

func TestBinService_ListVisibleByLocation_RepositoryErrorWrapped(t *testing.T) {
	bins := newFakeBinReadWriter()
	bins.listByLocErr = errors.New("boom")
	svc := newBinService(bins, nil, nil)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	_, err := svc.ListVisibleByLocation(context.Background(), viewer, domain.NewLocationID())
	if err == nil {
		t.Fatal("ListVisibleByLocation() error = nil, want a wrapped error")
	}
}

func TestBinService_GetByID_Success_EnrichesView(t *testing.T) {
	bins := newFakeBinReadWriter()
	owner := identity.User{ID: identity.NewUserID(), DisplayName: "Maya", Color: identity.ColorIndigo}
	b := &domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Winter Clothes", OwnerID: &owner.ID}
	if err := bins.Create(context.Background(), b); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
	members := &fakeMemberDirectory{members: []identity.User{owner}}
	items := &fakeItemCounter{counts: map[domain.BinID]int{b.ID: 5}}
	svc := newBinService(bins, members, items)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	got, err := svc.GetByID(context.Background(), viewer, b.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Bin.ID != b.ID {
		t.Errorf("GetByID() Bin.ID = %v, want %v", got.Bin.ID, b.ID)
	}
	if got.Owner == nil || got.Owner.Name != "Maya" {
		t.Errorf("GetByID() Owner = %+v, want Maya", got.Owner)
	}
	if got.ItemCount != 5 {
		t.Errorf("GetByID() ItemCount = %d, want 5", got.ItemCount)
	}
}

func TestBinService_GetByCode_Success_EnrichesView(t *testing.T) {
	bins := newFakeBinReadWriter()
	b := &domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Winter Clothes"}
	if err := bins.Create(context.Background(), b); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
	svc := newBinService(bins, nil, nil)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	got, err := svc.GetByCode(context.Background(), viewer, "a1")
	if err != nil {
		t.Fatalf("GetByCode: %v", err)
	}
	if got.Bin.ID != b.ID {
		t.Errorf("GetByCode() Bin.ID = %v, want %v", got.Bin.ID, b.ID)
	}
}

func TestBinService_GetByID_NotFoundWrapped(t *testing.T) {
	svc := newBinService(newFakeBinReadWriter(), nil, nil)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	_, err := svc.GetByID(context.Background(), viewer, domain.NewBinID())
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("GetByID(unknown) error = %v, want wrapped ErrBinNotFound", err)
	}
}

func TestBinService_GetByCode_NotFoundWrapped(t *testing.T) {
	svc := newBinService(newFakeBinReadWriter(), nil, nil)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	_, err := svc.GetByCode(context.Background(), viewer, "GHOST")
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("GetByCode(unknown) error = %v, want wrapped ErrBinNotFound", err)
	}
}

func TestBinService_Create_NormalizesCodeAndValidates(t *testing.T) {
	bins := newFakeBinReadWriter()
	svc := newBinService(bins, nil, nil)
	creator := identity.NewUserID()

	b, err := svc.Create(context.Background(), app.CreateBinInput{
		Code: "  a1  ", Name: "Winter Clothes", LocationID: domain.NewLocationID(), Visibility: domain.VisibilityPublic, CreatedBy: creator,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if b.Code != "A1" {
		t.Errorf("Create() Code = %q, want normalized %q", b.Code, "A1")
	}
}

func TestBinService_Create_ValidationRejected(t *testing.T) {
	svc := newBinService(newFakeBinReadWriter(), nil, nil)

	_, err := svc.Create(context.Background(), app.CreateBinInput{
		Code: "A1", Name: "  ", LocationID: domain.NewLocationID(), Visibility: domain.VisibilityPublic, CreatedBy: identity.NewUserID(),
	})
	if !errors.Is(err, domain.ErrInvalidBin) {
		t.Errorf("Create(blank name) error = %v, want ErrInvalidBin", err)
	}
}

func TestBinService_Create_RepositoryErrorWrapped(t *testing.T) {
	bins := newFakeBinReadWriter()
	bins.createErr = domain.ErrDuplicateBinCode
	svc := newBinService(bins, nil, nil)

	_, err := svc.Create(context.Background(), app.CreateBinInput{
		Code: "A1", Name: "Name", LocationID: domain.NewLocationID(), Visibility: domain.VisibilityPublic, CreatedBy: identity.NewUserID(),
	})
	if !errors.Is(err, domain.ErrDuplicateBinCode) {
		t.Errorf("Create() error = %v, want wrapped ErrDuplicateBinCode", err)
	}
}

func TestBinService_Edit_NotFoundWrapped(t *testing.T) {
	svc := newBinService(newFakeBinReadWriter(), nil, nil)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	err := svc.Edit(context.Background(), viewer, domain.NewBinID(), "Name", "", nil, domain.VisibilityPublic)
	if !errors.Is(err, domain.ErrBinNotFound) {
		t.Errorf("Edit(unknown) error = %v, want wrapped ErrBinNotFound", err)
	}
}

func TestBinService_Edit_NeverTouchesCodeOrLocation(t *testing.T) {
	bins := newFakeBinReadWriter()
	loc := domain.NewLocationID()
	b := &domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Old", LocationID: loc, Visibility: domain.VisibilityPublic}
	if err := bins.Create(context.Background(), b); err != nil {
		t.Fatalf("seed: %v", err)
	}
	svc := newBinService(bins, nil, nil)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	if err := svc.Edit(context.Background(), viewer, b.ID, "New Name", "desc", nil, domain.VisibilityPrivate); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	got := bins.bins[b.ID]
	if got.Name != "New Name" || got.Description != "desc" || got.Visibility != domain.VisibilityPrivate {
		t.Errorf("Edit did not update the bin: %+v", got)
	}
	if got.Code != "A1" || got.LocationID != loc {
		t.Errorf("Edit must never touch code/location: %+v", got)
	}
}

func TestBinService_Delete_Success(t *testing.T) {
	bins := newFakeBinReadWriter()
	b := &domain.Bin{ID: domain.NewBinID(), Code: "A1", Name: "Winter Clothes"}
	if err := bins.Create(context.Background(), b); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
	svc := newBinService(bins, nil, nil)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	if err := svc.Delete(context.Background(), viewer, b.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := bins.bins[b.ID]; ok {
		t.Error("Delete did not remove the bin via the repository")
	}
}

func TestBinService_Delete_NotEmptyWrapped(t *testing.T) {
	bins := newFakeBinReadWriter()
	bins.deleteErr = domain.ErrBinNotEmpty
	svc := newBinService(bins, nil, nil)
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	err := svc.Delete(context.Background(), viewer, domain.NewBinID())
	if !errors.Is(err, domain.ErrBinNotEmpty) {
		t.Errorf("Delete() error = %v, want wrapped ErrBinNotEmpty", err)
	}
}
