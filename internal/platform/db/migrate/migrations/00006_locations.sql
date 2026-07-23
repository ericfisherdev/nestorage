-- +goose Up
-- Storage locations (NSTR-26): a location is an area of the house that bins
-- sit in ("Garage", "Hall closet"). This is Sprint 4's first migration,
-- establishing the single internal/storage bounded context that NSTR-27
-- (bins) and NSTR-28 (items) build onto.
--
-- parent_id is nullable and self-referential from day one so nested areas
-- (e.g. "Garage" containing "Garage / Shelf B") can be enabled later without
-- a migration against live data, even though nothing surfaces nesting in the
-- UI yet. ON DELETE RESTRICT means deleting a location that still has a
-- child fails at the database — the mechanism behind
-- LocationRepository.Delete's ErrLocationNotEmpty guard. Every future
-- referrer (bins from NSTR-27, items from NSTR-28) means the same thing:
-- the location is not empty, so it uses the same RESTRICT behavior.
--
-- created_by references app_user (id) ON DELETE RESTRICT; identity
-- soft-deletes users via SetActive and never hard-deletes them, so this
-- restriction never actually fires — it exists to make the invariant
-- explicit rather than to be exercised.
--
-- "location" is not a reserved word in Postgres 17 (unlike "user", which
-- forced the app_user rename in 00002_identity.sql), so no rename is
-- needed here.
CREATE TABLE location (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    parent_id   uuid        REFERENCES location (id) ON DELETE RESTRICT,
    created_by  uuid        NOT NULL REFERENCES app_user (id) ON DELETE RESTRICT,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Supports the parent/child lookups the delete guard and future nested-area
-- browsing both rely on.
CREATE INDEX location_parent_id_idx ON location (parent_id);

-- +goose Down
DROP INDEX IF EXISTS location_parent_id_idx;
DROP TABLE IF EXISTS location;
