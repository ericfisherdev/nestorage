package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// itemDetailSearcher is the narrow port (ISP) ItemQueryService depends on,
// satisfied by domain.ItemRepository (a superset, via FindVisibleDetail/
// SearchVisible) and by test fakes. Deliberately excludes every mutation
// method domain.ItemRepository also carries: this service only ever reads.
type itemDetailSearcher interface {
	FindVisibleDetail(ctx context.Context, viewer identity.Principal, id domain.ItemID) (*domain.ItemDetailResult, error)
	SearchVisible(ctx context.Context, viewer identity.Principal, query string, limit int) ([]domain.ItemSearchResult, error)
}

// ItemQueryService implements the item detail and search reads NSTR-32's web
// handlers consume: a visibility-scoped single-item lookup and a
// trigram-backed type-ahead search. Like every other service in this
// package, it never reads a session, header, or cookie itself — the handler
// resolves the acting identity.Principal and passes it in.
type ItemQueryService struct {
	items  itemDetailSearcher
	logger *slog.Logger
}

// NewItemQueryService constructs ItemQueryService. Both dependencies are
// required; a missing one panics at construction time, matching every other
// constructor in this codebase (see NewItemService).
func NewItemQueryService(items itemDetailSearcher, logger *slog.Logger) *ItemQueryService {
	if items == nil {
		panic("storage/app: NewItemQueryService requires a non-nil itemDetailSearcher")
	}
	if logger == nil {
		panic("storage/app: NewItemQueryService requires a non-nil logger")
	}
	return &ItemQueryService{items: items, logger: logger}
}

// Detail returns id's detail read model, scoped to what viewer may see.
// Returns a wrapped domain.ErrItemNotFound when id is unknown or not visible
// to viewer.
func (s *ItemQueryService) Detail(ctx context.Context, viewer identity.Principal, id domain.ItemID) (*domain.ItemDetailResult, error) {
	result, err := s.items.FindVisibleDetail(ctx, viewer, id)
	if err != nil {
		return nil, fmt.Errorf("app: get item detail: %w", err)
	}
	return result, nil
}

// Search returns every item viewer may see matching rawQuery, trimmed of
// surrounding whitespace. A term shorter than domain.MinSearchQueryLength
// answers an empty slice without a database round trip — pg_trgm extracts no
// trigrams from (and so cannot accelerate a search over) a shorter pattern,
// see domain.MinSearchQueryLength's own doc. utf8.RuneCountInString, not
// len, measures the term: a byte count would reject a valid short term whose
// first character is multi-byte UTF-8.
func (s *ItemQueryService) Search(ctx context.Context, viewer identity.Principal, rawQuery string) ([]domain.ItemSearchResult, error) {
	query := strings.TrimSpace(rawQuery)
	if utf8.RuneCountInString(query) < domain.MinSearchQueryLength {
		s.logger.DebugContext(ctx, "storage: item search: term too short", "query_len", utf8.RuneCountInString(query))
		return []domain.ItemSearchResult{}, nil
	}

	results, err := s.items.SearchVisible(ctx, viewer, query, domain.DefaultSearchResultLimit)
	if err != nil {
		return nil, fmt.Errorf("app: search items: %w", err)
	}
	s.logger.DebugContext(ctx, "storage: item search", "result_count", len(results))
	return results, nil
}
