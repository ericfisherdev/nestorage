package components_test

import (
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/web/components"
)

func TestItemSearchPage_RendersSearchBoxWiredForTypeAhead(t *testing.T) {
	out := renderString(t, components.ItemSearchPage(components.SearchPageView{}))

	if !strings.Contains(out, `hx-get="/search"`) {
		t.Errorf("ItemSearchPage() search box missing hx-get: %s", out)
	}
	if !strings.Contains(out, `hx-trigger="input changed delay:400ms, search"`) {
		t.Errorf("ItemSearchPage() search box missing the debounced hx-trigger: %s", out)
	}
	if !strings.Contains(out, `hx-target="#search-results"`) {
		t.Errorf("ItemSearchPage() search box missing hx-target: %s", out)
	}
	if !strings.Contains(out, `hx-sync="this:replace"`) {
		t.Errorf("ItemSearchPage() search box missing hx-sync: %s", out)
	}
	if !strings.Contains(out, `name="q"`) {
		t.Errorf("ItemSearchPage() search box missing its name attribute: %s", out)
	}
	if !strings.Contains(out, `id="search-results"`) {
		t.Error("ItemSearchPage() missing the results div id the search box's hx-target relies on")
	}
}

func TestSearchResults_EmptyQuery_ShowsTypeToSearch(t *testing.T) {
	out := renderString(t, components.SearchResults(components.SearchPageView{}))
	if !strings.Contains(out, "Type to search.") {
		t.Errorf("SearchResults(empty query) = %s, want the type-to-search empty state", out)
	}
}

func TestSearchResults_NoMatches_ShowsNoMatchesMessage(t *testing.T) {
	view := components.SearchPageView{Query: "zzz-nomatch", Results: nil}
	out := renderString(t, components.SearchResults(view))
	if !strings.Contains(out, `No matches for &quot;zzz-nomatch&quot;.`) && !strings.Contains(out, `No matches for "zzz-nomatch".`) {
		t.Errorf("SearchResults(no matches) = %s, want a no-matches message naming the query", out)
	}
}

func TestSearchResults_WithResults_RendersEachRow(t *testing.T) {
	view := components.SearchPageView{
		Query: "stove",
		Results: []components.SearchResultView{
			{ID: "item-1", Name: "Camping stove", Quantity: 2, BinCode: "BIN-A01", LocationName: "Garage"},
			{ID: "item-2", Name: "Backup stove", Quantity: 1, CheckedOut: true, HolderName: "Maya"},
		},
	}
	out := renderString(t, components.SearchResults(view))

	if !strings.Contains(out, `href="/items/item-1"`) || !strings.Contains(out, "Camping stove") {
		t.Errorf("SearchResults() missing the in-bin result row: %s", out)
	}
	if !strings.Contains(out, "BIN-A01") || !strings.Contains(out, "Garage") {
		t.Errorf("SearchResults() in-bin row missing bin code/location: %s", out)
	}
	if !strings.Contains(out, `href="/items/item-2"`) || !strings.Contains(out, "Backup stove") {
		t.Errorf("SearchResults() missing the checked-out result row: %s", out)
	}
	if !strings.Contains(out, "Held by Maya") {
		t.Errorf("SearchResults() checked-out row missing holder: %s", out)
	}
	if !strings.Contains(out, "×2") || !strings.Contains(out, "×1") {
		t.Errorf("SearchResults() missing quantity markers: %s", out)
	}
}

func TestSearchResults_HasSearchIndicator(t *testing.T) {
	out := renderString(t, components.SearchResults(components.SearchPageView{}))
	if !strings.Contains(out, `id="search-indicator"`) || !strings.Contains(out, "htmx-indicator") {
		t.Errorf("SearchResults() missing the htmx-indicator span: %s", out)
	}
	if !strings.Contains(out, "Searching") {
		t.Errorf("SearchResults() indicator missing its label text: %s", out)
	}
}
