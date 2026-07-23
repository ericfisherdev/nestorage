-- +goose Up
-- Items (NSTR-28): the third aggregate in the internal/storage bounded
-- context 00006_locations.sql established, sitting on top of bin (NSTR-27).
-- An item sits in exactly one bin OR is checked out to exactly one person —
-- never both, never neither — enforced below by item_placement_exclusive,
-- not only by the domain's mirroring Item.Validate.
--
-- id has no DEFAULT: the app supplies a UUIDv7 (domain.NewItemID), matching
-- bin and device_token/api_key rather than location/app_user's
-- gen_random_uuid() default, for B-tree index locality on inserts.
--
-- description is nullable (the item's optional description), unlike
-- bin.description's NOT NULL DEFAULT '' — an item genuinely may have none,
-- scanned into *string by the adapter.
--
-- quantity is integer NOT NULL CHECK (quantity > 0), named
-- item_quantity_check so the adapter can distinguish it, via
-- pgconn.PgError.ConstraintName, from item_placement_exclusive — both are
-- SQLSTATE 23514 (check_violation), so the constraint name is the only way
-- to tell them apart. The domain re-checks this too (Item.Validate), so the
-- rejection is not only a database-level guard.
--
-- current_bin_id and held_by are both nullable, and item_placement_exclusive
-- uses num_nonnulls(current_bin_id, held_by) = 1 to require exactly one of
-- the two set — one CHECK covers both "never both" and "never neither"
-- (verified against PostgreSQL 17's functions-comparison table, the same
-- num_nonnulls rationale 00007_bin.sql documents for its own visibility
-- column defaults).
--
-- current_bin_id references bin (id) ON DELETE RESTRICT: an item outlives
-- removal from a bin (it becomes held, never deleted), so a bin must be
-- emptied of its items before it can be deleted — the item-side half of the
-- bin non-empty guard 00007_bin.sql's own comment anticipated. held_by and
-- created_by reference app_user (id) ON DELETE RESTRICT; identity
-- soft-deletes users via SetActive and never hard-deletes them, so this
-- restriction is a formality, the same rationale 00006/00007 give for their
-- own app_user foreign keys.
--
-- placement_changed_at defaults to now() alongside created_at/updated_at —
-- all three read the same statement-level timestamp on INSERT, so a
-- freshly created item's "held/placed since" (NSTR-32) starts exactly at
-- its creation time. NSTR-29 advances it on every add/remove/return; it is
-- never derived from updated_at, because updated_at also changes on a
-- plain name/description/quantity edit (Update), which must never look
-- like a placement change.
--
-- All three foreign keys are named explicitly (matching bin's
-- bin_location_id_fkey/bin_owner_id_fkey/bin_created_by_fkey) so the
-- adapter can distinguish, via pgconn.PgError.ConstraintName, an unknown bin
-- from an unknown holder/creator.
--
-- Blank checks use length(btrim(x)) > 0, never x <> '' — the SonarCloud
-- plsql:NullComparison false positive documented in 00004_device_token.sql.
CREATE TABLE item (
    id                   uuid        PRIMARY KEY,
    name                 text        NOT NULL CHECK (length(btrim(name)) > 0),
    description          text,
    quantity             integer     NOT NULL,
    current_bin_id       uuid,
    held_by              uuid,
    created_by           uuid        NOT NULL,
    placement_changed_at timestamptz NOT NULL DEFAULT now(),
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT item_quantity_check CHECK (quantity > 0),
    CONSTRAINT item_placement_exclusive CHECK (num_nonnulls(current_bin_id, held_by) = 1),
    CONSTRAINT item_current_bin_id_fkey FOREIGN KEY (current_bin_id) REFERENCES bin (id) ON DELETE RESTRICT,
    CONSTRAINT item_held_by_fkey FOREIGN KEY (held_by) REFERENCES app_user (id) ON DELETE RESTRICT,
    CONSTRAINT item_created_by_fkey FOREIGN KEY (created_by) REFERENCES app_user (id) ON DELETE RESTRICT
);

-- Backs the visibility-aware JOIN to bin (Get/ListByBin) and the held-item
-- lookups NSTR-29/32 add; partial (WHERE ... IS NOT NULL) since every row
-- always has exactly one of the two columns null.
CREATE INDEX item_current_bin_idx ON item (current_bin_id) WHERE current_bin_id IS NOT NULL;
CREATE INDEX item_held_by_idx ON item (held_by) WHERE held_by IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS item_held_by_idx;
DROP INDEX IF EXISTS item_current_bin_idx;
DROP TABLE IF EXISTS item;
