package domain

import (
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// MinSearchQueryLength is the shortest search term app.ItemQueryService.Search
// will actually query for. pg_trgm extracts no trigrams from (and so falls
// back to an unaccelerated full scan for) a pattern under three characters
// (verified against the PostgreSQL 16 pgtrgm docs — see 00009_item_search.sql's
// own comment), so a term shorter than this is treated as "no results yet"
// rather than ever reaching ItemRepository.SearchVisible.
const MinSearchQueryLength = 3

// DefaultSearchResultLimit bounds how many rows SearchVisible returns for
// app.ItemQueryService's own type-ahead results list — kept here, next to
// MinSearchQueryLength, so the app layer's cap and this doc cannot drift
// apart.
const DefaultSearchResultLimit = 25

// ItemDetailResult is the read model ItemRepository.FindVisibleDetail
// returns: an item's own fields, plus its current bin's name/code and
// location name (when in a bin) or its holder's display name/color (when
// checked out) — the joined columns Item alone (ids only) does not carry.
// Exactly one of BinName/BinCode/LocationName or HolderName/HolderColor is
// populated, mirroring Item's own exactly-one-of CurrentBinID/HeldBy
// invariant (see Item.State).
type ItemDetailResult struct {
	Item         Item
	BinName      string
	BinCode      string
	LocationName string
	HolderName   string
	HolderColor  identity.UserColor
}

// ItemSearchResult is the per-row read model ItemRepository.SearchVisible
// returns: an item's id, name, quantity, and derived placement state, plus
// either its bin's code and location name or its holder's display name —
// the exact row shape the search results list renders.
type ItemSearchResult struct {
	ID           ItemID
	Name         string
	Quantity     int
	State        PlacementState
	BinCode      string
	LocationName string
	HolderName   string
}
