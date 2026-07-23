package app_test

import (
	"context"
	"errors"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// fakeItemDetailSearcher is a configurable itemDetailSearcher fake for
// ItemQueryService's hermetic unit tests, mirroring fakeItemRepo's own
// shape in item_test.go.
type fakeItemDetailSearcher struct {
	detail    *domain.ItemDetailResult
	detailErr error

	searchResults []domain.ItemSearchResult
	searchErr     error

	// searchCalls records every query SearchVisible was actually invoked
	// with, so a test can assert Search's min-length gate never reaches the
	// repository for a too-short term.
	searchCalls []string
}

func (f *fakeItemDetailSearcher) FindVisibleDetail(_ context.Context, _ identity.Principal, _ domain.ItemID) (*domain.ItemDetailResult, error) {
	if f.detailErr != nil {
		return nil, f.detailErr
	}
	return f.detail, nil
}

func (f *fakeItemDetailSearcher) SearchVisible(_ context.Context, _ identity.Principal, query string, _ int) ([]domain.ItemSearchResult, error) {
	f.searchCalls = append(f.searchCalls, query)
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.searchResults, nil
}

func testViewer() identity.Principal {
	return identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")
}

func TestNewItemQueryService_PanicsOnNilDeps(t *testing.T) {
	// A literal nil is passed directly as the interface argument in each
	// case below (rather than a typed nil *fakeItemDetailSearcher variable)
	// to avoid Go's classic typed-nil-interface gotcha: an interface value
	// wrapping a nil concrete pointer does not itself compare equal to nil,
	// which would defeat NewItemQueryService's own nil check — mirrors
	// TestNewItemService_PanicsOnNilDeps' identical precaution.
	t.Run("nil repository", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewItemQueryService(nil, ...) did not panic")
			}
		}()
		app.NewItemQueryService(nil, testLogger())
	})
	t.Run("nil logger", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewItemQueryService(..., nil) did not panic")
			}
		}()
		app.NewItemQueryService(&fakeItemDetailSearcher{}, nil)
	})
}

func TestItemQueryService_Detail(t *testing.T) {
	itemID := domain.NewItemID()
	fake := &fakeItemDetailSearcher{detail: &domain.ItemDetailResult{Item: domain.Item{ID: itemID, Name: "Stove"}}}
	svc := app.NewItemQueryService(fake, testLogger())

	got, err := svc.Detail(context.Background(), testViewer(), itemID)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if got.Item.ID != itemID || got.Item.Name != "Stove" {
		t.Errorf("Detail() = %+v, want the fake's item", got)
	}
}

func TestItemQueryService_Detail_NotFoundWrapped(t *testing.T) {
	fake := &fakeItemDetailSearcher{detailErr: domain.ErrItemNotFound}
	svc := app.NewItemQueryService(fake, testLogger())

	_, err := svc.Detail(context.Background(), testViewer(), domain.NewItemID())
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("Detail(unknown) error = %v, want wrapped ErrItemNotFound", err)
	}
}

func TestItemQueryService_Search_TrimsAndDelegates(t *testing.T) {
	fake := &fakeItemDetailSearcher{searchResults: []domain.ItemSearchResult{{Name: "Camping stove"}}}
	svc := app.NewItemQueryService(fake, testLogger())

	got, err := svc.Search(context.Background(), testViewer(), "  stove  ")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Camping stove" {
		t.Errorf("Search() = %+v, want the fake's one result", got)
	}
	if len(fake.searchCalls) != 1 || fake.searchCalls[0] != "stove" {
		t.Errorf("SearchVisible called with %v, want the trimmed term [\"stove\"]", fake.searchCalls)
	}
}

// TestItemQueryService_Search_ShortTermNeverReachesRepository proves the
// trigram minimum-length gate: a term under domain.MinSearchQueryLength
// answers an empty slice without ever calling SearchVisible — pg_trgm would
// extract no trigrams from it anyway (see domain.MinSearchQueryLength's own
// doc), so the round trip would be wasted.
func TestItemQueryService_Search_ShortTermNeverReachesRepository(t *testing.T) {
	fake := &fakeItemDetailSearcher{searchResults: []domain.ItemSearchResult{{Name: "should not be returned"}}}
	svc := app.NewItemQueryService(fake, testLogger())

	tests := []string{"", " ", "a", "ab", "  ab  "}
	for _, term := range tests {
		got, err := svc.Search(context.Background(), testViewer(), term)
		if err != nil {
			t.Fatalf("Search(%q): %v", term, err)
		}
		if len(got) != 0 {
			t.Errorf("Search(%q) = %v, want an empty slice for a too-short term", term, got)
		}
	}
	if len(fake.searchCalls) != 0 {
		t.Errorf("SearchVisible called %d times for too-short terms, want 0", len(fake.searchCalls))
	}
}

func TestItemQueryService_Search_RepositoryErrorWrapped(t *testing.T) {
	fake := &fakeItemDetailSearcher{searchErr: errors.New("boom")}
	svc := app.NewItemQueryService(fake, testLogger())

	_, err := svc.Search(context.Background(), testViewer(), "stove")
	if err == nil {
		t.Fatal("Search() error = nil, want the wrapped repository error")
	}
}

// TestItemQueryService_Search_TooShortAnswersEmptySliceNotNil proves the
// min-length gate's own short-circuit answers a non-nil empty slice (never
// nil) even though it never reaches the fake at all — a caller ranging over
// the result should never need a nil check either way.
func TestItemQueryService_Search_TooShortAnswersEmptySliceNotNil(t *testing.T) {
	fake := &fakeItemDetailSearcher{}
	svc := app.NewItemQueryService(fake, testLogger())

	got, err := svc.Search(context.Background(), testViewer(), "a")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got == nil {
		t.Error("Search(\"a\") returned nil, want a non-nil empty slice")
	}
}
