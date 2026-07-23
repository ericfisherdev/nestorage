package app_test

import (
	"context"
	"errors"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// fakeLocationRepo is an in-memory locationRepository fake for
// LocationService's hermetic unit tests, mirroring fakeItemRepo's shape.
type fakeLocationRepo struct {
	locations map[domain.LocationID]*domain.Location

	createErr error
	findErr   error
	listErr   error
	renameErr error
	deleteErr error
}

func newFakeLocationRepo() *fakeLocationRepo {
	return &fakeLocationRepo{locations: make(map[domain.LocationID]*domain.Location)}
}

func (f *fakeLocationRepo) Create(_ context.Context, l *domain.Location) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.locations[l.ID] = l
	return nil
}

func (f *fakeLocationRepo) FindVisibleByID(_ context.Context, _ identity.Principal, id domain.LocationID) (*domain.Location, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	l, ok := f.locations[id]
	if !ok {
		return nil, domain.ErrLocationNotFound
	}
	return l, nil
}

func (f *fakeLocationRepo) List(_ context.Context) ([]domain.Location, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	locations := make([]domain.Location, 0, len(f.locations))
	for _, l := range f.locations {
		locations = append(locations, *l)
	}
	return locations, nil
}

func (f *fakeLocationRepo) Rename(_ context.Context, id domain.LocationID, name string) error {
	if f.renameErr != nil {
		return f.renameErr
	}
	l, ok := f.locations[id]
	if !ok {
		return domain.ErrLocationNotFound
	}
	l.Name = name
	return nil
}

func (f *fakeLocationRepo) Delete(_ context.Context, id domain.LocationID) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.locations[id]; !ok {
		return domain.ErrLocationNotFound
	}
	delete(f.locations, id)
	return nil
}

// fakeBinLister is an in-memory binLister fake for LocationService's
// per-location bin-count enrichment.
type fakeBinLister struct {
	bins []domain.Bin
	err  error
}

func (f *fakeBinLister) ListVisible(_ context.Context, _ identity.Principal) ([]domain.Bin, error) {
	return f.bins, f.err
}

func TestNewLocationService_PanicsOnNilDeps(t *testing.T) {
	t.Run("nil locationRepository", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewLocationService did not panic")
			}
		}()
		app.NewLocationService(nil, &fakeBinLister{}, testLogger())
	})
	t.Run("nil binLister", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewLocationService did not panic")
			}
		}()
		app.NewLocationService(newFakeLocationRepo(), nil, testLogger())
	})
	t.Run("nil logger", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewLocationService did not panic")
			}
		}()
		app.NewLocationService(newFakeLocationRepo(), &fakeBinLister{}, nil)
	})
}

func TestLocationService_List_CarriesBinCounts(t *testing.T) {
	locs := newFakeLocationRepo()
	garage := &domain.Location{ID: domain.NewLocationID(), Name: "Garage"}
	attic := &domain.Location{ID: domain.NewLocationID(), Name: "Attic"}
	if err := locs.Create(context.Background(), garage); err != nil {
		t.Fatalf("seed garage: %v", err)
	}
	if err := locs.Create(context.Background(), attic); err != nil {
		t.Fatalf("seed attic: %v", err)
	}

	bins := &fakeBinLister{bins: []domain.Bin{
		{ID: domain.NewBinID(), LocationID: garage.ID},
		{ID: domain.NewBinID(), LocationID: garage.ID},
		{ID: domain.NewBinID(), LocationID: attic.ID},
	}}
	svc := app.NewLocationService(locs, bins, testLogger())
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	got, err := svc.List(context.Background(), viewer)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	counts := make(map[domain.LocationID]int, len(got))
	for _, s := range got {
		counts[s.Location.ID] = s.BinCount
	}
	if counts[garage.ID] != 2 {
		t.Errorf("List() garage BinCount = %d, want 2", counts[garage.ID])
	}
	if counts[attic.ID] != 1 {
		t.Errorf("List() attic BinCount = %d, want 1", counts[attic.ID])
	}
}

func TestLocationService_List_RepositoryErrorWrapped(t *testing.T) {
	locs := newFakeLocationRepo()
	locs.listErr = errors.New("boom")
	svc := app.NewLocationService(locs, &fakeBinLister{}, testLogger())

	_, err := svc.List(context.Background(), identity.Principal{})
	if err == nil {
		t.Fatal("List() error = nil, want a wrapped error")
	}
}

func TestLocationService_Get_NotFoundWrapped(t *testing.T) {
	svc := app.NewLocationService(newFakeLocationRepo(), &fakeBinLister{}, testLogger())
	viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")

	_, err := svc.Get(context.Background(), viewer, domain.NewLocationID())
	if !errors.Is(err, domain.ErrLocationNotFound) {
		t.Errorf("Get(unknown) error = %v, want wrapped ErrLocationNotFound", err)
	}
}

func TestLocationService_Create(t *testing.T) {
	locs := newFakeLocationRepo()
	svc := app.NewLocationService(locs, &fakeBinLister{}, testLogger())
	creator := identity.NewUserID()

	l, err := svc.Create(context.Background(), "  Garage  ", "Main garage", nil, creator)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if l.Name != "Garage" {
		t.Errorf("Create() Name = %q, want trimmed %q", l.Name, "Garage")
	}
	if _, ok := locs.locations[l.ID]; !ok {
		t.Error("Create did not persist the location via the repository")
	}
}

func TestLocationService_Create_ValidationRejected(t *testing.T) {
	svc := app.NewLocationService(newFakeLocationRepo(), &fakeBinLister{}, testLogger())

	_, err := svc.Create(context.Background(), "   ", "", nil, identity.NewUserID())
	if !errors.Is(err, domain.ErrInvalidLocationName) {
		t.Errorf("Create(blank name) error = %v, want ErrInvalidLocationName", err)
	}
}

func TestLocationService_Rename_ValidationRejected(t *testing.T) {
	svc := app.NewLocationService(newFakeLocationRepo(), &fakeBinLister{}, testLogger())

	err := svc.Rename(context.Background(), domain.NewLocationID(), "  ")
	if !errors.Is(err, domain.ErrInvalidLocationName) {
		t.Errorf("Rename(blank name) error = %v, want ErrInvalidLocationName", err)
	}
}

func TestLocationService_Rename_NotFoundWrapped(t *testing.T) {
	svc := app.NewLocationService(newFakeLocationRepo(), &fakeBinLister{}, testLogger())

	err := svc.Rename(context.Background(), domain.NewLocationID(), "Garage")
	if !errors.Is(err, domain.ErrLocationNotFound) {
		t.Errorf("Rename(unknown) error = %v, want wrapped ErrLocationNotFound", err)
	}
}

func TestLocationService_Delete_NotEmptyWrapped(t *testing.T) {
	locs := newFakeLocationRepo()
	locs.deleteErr = domain.ErrLocationNotEmpty
	svc := app.NewLocationService(locs, &fakeBinLister{}, testLogger())

	err := svc.Delete(context.Background(), domain.NewLocationID())
	if !errors.Is(err, domain.ErrLocationNotEmpty) {
		t.Errorf("Delete() error = %v, want wrapped ErrLocationNotEmpty", err)
	}
}
