-- +goose Up
-- Item search (NSTR-32): pg_trgm-accelerated substring/type-ahead matching
-- over item name/description and, via the search JOIN, bin name and
-- location name — the same tables 00006/00007/00008 already created.
--
-- ILIKE '%q%' backed by a GIN trigram index, NOT tsvector full-text search:
-- verified against the PostgreSQL 16 pgtrgm docs, a GIN index built with
-- gin_trgm_ops extracts trigrams from the search PATTERN, which is exactly
-- what accelerates a substring LIKE/ILIKE/regex match — the type-ahead a
-- household member types over short item/bin/location names needs. tsvector
-- is lexeme/word-based and stems whole tokens, the wrong tool for a partial
-- product/brand name mid-word. A pattern shorter than three characters
-- extracts no trigrams and falls back to a full scan (the same doc);
-- app.ItemQueryService.Search enforces that minimum in Go so a one/two-
-- character term never reaches these indexes at all.
--
-- The extension is created once, here, with IF NOT EXISTS; the down
-- migration below deliberately does not drop it — dropping a shared
-- extension in a per-feature down migration is unsafe, since a later
-- migration could come to depend on it before this one is ever rolled back.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX item_name_trgm ON item USING gin (name gin_trgm_ops);
CREATE INDEX item_description_trgm ON item USING gin (description gin_trgm_ops);
CREATE INDEX bin_name_trgm ON bin USING gin (name gin_trgm_ops);
CREATE INDEX location_name_trgm ON location USING gin (name gin_trgm_ops);

-- +goose Down
DROP INDEX IF EXISTS location_name_trgm;
DROP INDEX IF EXISTS bin_name_trgm;
DROP INDEX IF EXISTS item_description_trgm;
DROP INDEX IF EXISTS item_name_trgm;
