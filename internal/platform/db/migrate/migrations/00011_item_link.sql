-- +goose Up
-- Labeled URLs on items (NSTR-38): item_link is a child entity of item, not
-- a bounded-context table of its own — it lives in internal/storage (not
-- internal/media, which is photo storage only) and is fetched, authorized,
-- and mutated alongside the item it belongs to, inheriting the item's
-- visibility rather than carrying any of its own.
--
-- id has no DEFAULT: the app supplies a UUIDv7 (domain.NewItemLinkID),
-- matching item/bin/photo rather than location/app_user's
-- gen_random_uuid() default, for B-tree index locality on inserts.
--
-- item_id ON DELETE CASCADE: links are owned, intra-aggregate child rows
-- (unlike item's own cross-aggregate RESTRICT fks to bin/app_user) — deleting
-- an item deletes its links with it.
--
-- Blank checks use length(btrim(x)) > 0, never x <> '', per the SonarCloud
-- plsql:NullComparison false-positive workaround documented in
-- 00004_device_token.sql.
--
-- No UNIQUE constraint on (item_id, position): allowing two links to share a
-- position keeps an insert from racing a reorder, and ORDER BY position, id
-- (mirroring every other list in this codebase) tie-breaks deterministically
-- when that happens.
CREATE TABLE item_link (
    id         uuid        PRIMARY KEY,
    item_id    uuid        NOT NULL,
    label      text        NOT NULL,
    url        text        NOT NULL,
    position   integer     NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT item_link_item_id_fkey FOREIGN KEY (item_id) REFERENCES item (id) ON DELETE CASCADE,
    CONSTRAINT item_link_label_check CHECK (length(btrim(label)) > 0),
    CONSTRAINT item_link_url_check CHECK (length(btrim(url)) > 0),
    CONSTRAINT item_link_position_check CHECK (position >= 0)
);

-- Backs ListByItem's per-item, position-ordered read.
CREATE INDEX item_link_item_id_idx ON item_link (item_id);

-- +goose Down
DROP INDEX IF EXISTS item_link_item_id_idx;
DROP TABLE IF EXISTS item_link;
