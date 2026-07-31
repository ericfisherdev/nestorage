-- +goose Up
-- Household scoping (NSTR-122): adds household_id to every storage/media
-- table that has no path to a household through its own row. Which table
-- carries it directly (rather than inheriting through a parent) was decided
-- from the migrations, not assumed — see the ticket's own per-table
-- rationale: location is the root of the containment chain; bin needs its
-- own column because bin_code_uniq must become per-household and
-- FindVisibleByCode/the public /b/CODE deep link resolve a scanned code
-- directly, without joining location first; item cannot inherit through
-- bin because item_placement_exclusive guarantees a held item has no bin at
-- all; item_event is a snapshot audit table with no FK to inherit through
-- (see 00012_item_event.sql's own comment); return_request is mutated by
-- bare request id and carries two member references; photo is served by
-- bare photo id. item_link and item_photo deliberately do NOT carry the
-- column: both are ON DELETE CASCADE children fetched and authorized only
-- alongside the item they belong to, so they inherit scope through item_id.
--
-- Scope decision (Eric, 2026-07-29): neither app is live, so there is no
-- production data to backfill — every column below is added NOT NULL
-- directly, with no backfill pass and no NULL-then-backfill-then-NOT-NULL
-- dance. This migration's Up is only safe against a freshly reset database
-- (zero households, zero rows): a database already holding rows in any of
-- the six tables below fails the corresponding ADD COLUMN immediately.
--
-- This is the schema half of a two-ticket split (NSTR-122 here,
-- NSTR-131 the follow-up): every composite tenant FK, the per-household
-- bin-code uniqueness, and the re-led hot indexes ship in THIS migration,
-- not the sweep's, because goose runs here without WithAllowOutofOrder
-- (verified against nestcore's db/migrate.New call site) — a later
-- migration filling this number's gap would break every environment that
-- already applied a migration numbered after it. Read/mutation filtering
-- (adding a WHERE household_id = ... predicate to any query) is NSTR-131's
-- job; this migration only reshapes the schema those predicates will use.

-- Reconcile identity.member's own composite-FK target, mirroring
-- 00017_identity_schema.sql's own cross-migration-set reconciliation
-- precedent: nestorage's gated tests apply ONLY this repo's own embedded
-- migration set (internal/platform/db/migrate), never nestcore's identity
-- migrations (identity/migrate/migrations/00001_identity_baseline.sql,
-- which adds this exact index under this exact name), while production
-- runs both Runners against the same database. IF NOT EXISTS makes this a
-- no-op in production regardless of which Runner reaches it first — see
-- 00017's own header for the "whichever runs first owns the schema, every
-- CREATE is IF NOT EXISTS" policy this follows.
CREATE UNIQUE INDEX IF NOT EXISTS member_household_id_id_uniq ON identity.member (household_id, id);

ALTER TABLE location       ADD COLUMN household_id uuid NOT NULL;
ALTER TABLE bin             ADD COLUMN household_id uuid NOT NULL;
ALTER TABLE item            ADD COLUMN household_id uuid NOT NULL;
ALTER TABLE item_event      ADD COLUMN household_id uuid NOT NULL;
ALTER TABLE return_request  ADD COLUMN household_id uuid NOT NULL;
ALTER TABLE photo           ADD COLUMN household_id uuid NOT NULL;

-- Composite-FK targets: every table a household-scoped FK below points at
-- needs a UNIQUE (household_id, id) so Postgres can validate the pair
-- together — mirroring identity.member_household_id_id_uniq exactly
-- (nestcore's NSTR-120 identity baseline, identity/migrate/migrations/00001_identity_baseline.sql).
ALTER TABLE location ADD CONSTRAINT location_household_id_id_uniq UNIQUE (household_id, id);
ALTER TABLE bin       ADD CONSTRAINT bin_household_id_id_uniq      UNIQUE (household_id, id);
ALTER TABLE item      ADD CONSTRAINT item_household_id_id_uniq     UNIQUE (household_id, id);

-- bin_code_uniq becomes per-household: two households may legitimately
-- print the same label code onto two different bins. Reusing the same
-- constraint name means BinRepository.Create's pgconn.PgError.ConstraintName
-- mapping (binCodeUniqueConstraint) needs no change at all.
ALTER TABLE bin DROP CONSTRAINT bin_code_uniq;
ALTER TABLE bin ADD CONSTRAINT bin_code_uniq UNIQUE (household_id, code);

-- location's self-referential parent FK: a child location can never cross
-- households. parent_id is nullable (a top-level location); Postgres's
-- default MATCH SIMPLE skips a multi-column FK check entirely whenever any
-- one of its columns is NULL (verified against the PostgreSQL 17 CREATE
-- TABLE docs), so a top-level location's NULL parent_id is unaffected —
-- the same mechanism the ticket calls out for item_current_bin_id_fkey
-- below.
ALTER TABLE location DROP CONSTRAINT location_parent_id_fkey;
ALTER TABLE location ADD CONSTRAINT location_parent_id_fkey
    FOREIGN KEY (household_id, parent_id) REFERENCES location (household_id, id) ON DELETE RESTRICT;

-- bin -> location, household-scoped: a bin can never sit in another
-- household's location.
ALTER TABLE bin DROP CONSTRAINT bin_location_id_fkey;
ALTER TABLE bin ADD CONSTRAINT bin_location_id_fkey
    FOREIGN KEY (household_id, location_id) REFERENCES location (household_id, id) ON DELETE RESTRICT;

-- item -> bin, household-scoped. current_bin_id is nullable (a held item
-- has no bin at all); MATCH SIMPLE lets a held item skip the check, the
-- same mechanism location_parent_id_fkey above relies on.
ALTER TABLE item DROP CONSTRAINT item_current_bin_id_fkey;
ALTER TABLE item ADD CONSTRAINT item_current_bin_id_fkey
    FOREIGN KEY (household_id, current_bin_id) REFERENCES bin (household_id, id) ON DELETE RESTRICT;

-- Member references re-pointed as (household_id, member_id) composite FKs
-- into identity.member, mirroring Nestova's own task_instance tenant FK
-- pattern and identity.member_household_id_id_uniq. Every ON DELETE
-- behavior is preserved exactly from its prior single-column form.
-- item_event.actor_user_id is deliberately NOT touched here: it stays the
-- plain FK 00017_identity_schema.sql already established (this table's own
-- household_id is a snapshot column, in the same spirit as item_name and
-- bin_label, with no FK relationship of its own — the same reasoning that
-- already exempts item_id/bin_id, see 00012_item_event.sql's own comment).
ALTER TABLE location DROP CONSTRAINT location_created_by_fkey;
ALTER TABLE location ADD CONSTRAINT location_created_by_fkey
    FOREIGN KEY (household_id, created_by) REFERENCES identity.member (household_id, id) ON DELETE RESTRICT;

ALTER TABLE bin DROP CONSTRAINT bin_owner_id_fkey;
ALTER TABLE bin ADD CONSTRAINT bin_owner_id_fkey
    FOREIGN KEY (household_id, owner_id) REFERENCES identity.member (household_id, id) ON DELETE RESTRICT;
ALTER TABLE bin DROP CONSTRAINT bin_created_by_fkey;
ALTER TABLE bin ADD CONSTRAINT bin_created_by_fkey
    FOREIGN KEY (household_id, created_by) REFERENCES identity.member (household_id, id) ON DELETE RESTRICT;

ALTER TABLE item DROP CONSTRAINT item_held_by_fkey;
ALTER TABLE item ADD CONSTRAINT item_held_by_fkey
    FOREIGN KEY (household_id, held_by) REFERENCES identity.member (household_id, id) ON DELETE RESTRICT;
ALTER TABLE item DROP CONSTRAINT item_created_by_fkey;
ALTER TABLE item ADD CONSTRAINT item_created_by_fkey
    FOREIGN KEY (household_id, created_by) REFERENCES identity.member (household_id, id) ON DELETE RESTRICT;

ALTER TABLE return_request DROP CONSTRAINT return_request_requester_id_fkey;
ALTER TABLE return_request ADD CONSTRAINT return_request_requester_id_fkey
    FOREIGN KEY (household_id, requester_id) REFERENCES identity.member (household_id, id) ON DELETE RESTRICT;
ALTER TABLE return_request DROP CONSTRAINT return_request_holder_id_fkey;
ALTER TABLE return_request ADD CONSTRAINT return_request_holder_id_fkey
    FOREIGN KEY (household_id, holder_id) REFERENCES identity.member (household_id, id) ON DELETE RESTRICT;

ALTER TABLE photo DROP CONSTRAINT photo_uploaded_by_fkey;
ALTER TABLE photo ADD CONSTRAINT photo_uploaded_by_fkey
    FOREIGN KEY (household_id, uploaded_by) REFERENCES identity.member (household_id, id) ON DELETE RESTRICT;

-- Re-lead the hot indexes with household_id, matched against the queries
-- that use them: NSTR-131 adds the household predicate those queries need
-- this leading column for; this migration only reshapes the index itself,
-- per the out-of-order restriction explained above.
DROP INDEX bin_location_idx;
CREATE INDEX bin_location_idx ON bin (household_id, location_id);
DROP INDEX bin_owner_idx;
CREATE INDEX bin_owner_idx ON bin (household_id, owner_id);

DROP INDEX item_current_bin_idx;
CREATE INDEX item_current_bin_idx ON item (household_id, current_bin_id) WHERE current_bin_id IS NOT NULL;
DROP INDEX item_held_by_idx;
CREATE INDEX item_held_by_idx ON item (household_id, held_by) WHERE held_by IS NOT NULL;

DROP INDEX item_event_item_occurred_idx;
CREATE INDEX item_event_item_occurred_idx ON item_event (household_id, item_id, occurred_at DESC, id DESC);
DROP INDEX item_event_bin_occurred_idx;
CREATE INDEX item_event_bin_occurred_idx ON item_event (household_id, bin_id, occurred_at DESC, id DESC) WHERE bin_id IS NOT NULL;

-- The search index: btree_gin lets a uuid equality column sit alongside a
-- gin_trgm_ops text column in one multicolumn GIN index (verified against
-- the PostgreSQL 17 btree_gin extension docs) — the mechanism NSTR-131's
-- household-scoped SearchVisible needs; a plain gin_trgm_ops index has no
-- operator class for a uuid column at all. Not dropped in Down, mirroring
-- 00009_item_search.sql's own pg_trgm rationale: dropping a shared
-- extension in a per-feature down migration is unsafe.
-- Schema-qualified for the same reason as pgcrypto in 00001_baseline.sql
-- (NSTR-119): search_path resolves nestorage before public, so an
-- unqualified CREATE EXTENSION would install into nestorage instead.
CREATE EXTENSION IF NOT EXISTS btree_gin SCHEMA public;
DROP INDEX item_name_trgm;
CREATE INDEX item_name_trgm ON item USING gin (household_id, name gin_trgm_ops);
DROP INDEX item_description_trgm;
CREATE INDEX item_description_trgm ON item USING gin (household_id, description gin_trgm_ops);
DROP INDEX bin_name_trgm;
CREATE INDEX bin_name_trgm ON bin USING gin (household_id, name gin_trgm_ops);
DROP INDEX location_name_trgm;
CREATE INDEX location_name_trgm ON location USING gin (household_id, name gin_trgm_ops);

-- +goose Down
DROP INDEX IF EXISTS location_name_trgm;
CREATE INDEX location_name_trgm ON location USING gin (name gin_trgm_ops);
DROP INDEX IF EXISTS bin_name_trgm;
CREATE INDEX bin_name_trgm ON bin USING gin (name gin_trgm_ops);
DROP INDEX IF EXISTS item_description_trgm;
CREATE INDEX item_description_trgm ON item USING gin (description gin_trgm_ops);
DROP INDEX IF EXISTS item_name_trgm;
CREATE INDEX item_name_trgm ON item USING gin (name gin_trgm_ops);

DROP INDEX IF EXISTS item_event_bin_occurred_idx;
CREATE INDEX item_event_bin_occurred_idx ON item_event (bin_id, occurred_at DESC, id DESC) WHERE bin_id IS NOT NULL;
DROP INDEX IF EXISTS item_event_item_occurred_idx;
CREATE INDEX item_event_item_occurred_idx ON item_event (item_id, occurred_at DESC, id DESC);

DROP INDEX IF EXISTS item_held_by_idx;
CREATE INDEX item_held_by_idx ON item (held_by) WHERE held_by IS NOT NULL;
DROP INDEX IF EXISTS item_current_bin_idx;
CREATE INDEX item_current_bin_idx ON item (current_bin_id) WHERE current_bin_id IS NOT NULL;

DROP INDEX IF EXISTS bin_owner_idx;
CREATE INDEX bin_owner_idx ON bin (owner_id);
DROP INDEX IF EXISTS bin_location_idx;
CREATE INDEX bin_location_idx ON bin (location_id);

ALTER TABLE photo DROP CONSTRAINT photo_uploaded_by_fkey;
ALTER TABLE photo ADD CONSTRAINT photo_uploaded_by_fkey
    FOREIGN KEY (uploaded_by) REFERENCES identity.member (id) ON DELETE RESTRICT;

ALTER TABLE return_request DROP CONSTRAINT return_request_holder_id_fkey;
ALTER TABLE return_request ADD CONSTRAINT return_request_holder_id_fkey
    FOREIGN KEY (holder_id) REFERENCES identity.member (id) ON DELETE RESTRICT;
ALTER TABLE return_request DROP CONSTRAINT return_request_requester_id_fkey;
ALTER TABLE return_request ADD CONSTRAINT return_request_requester_id_fkey
    FOREIGN KEY (requester_id) REFERENCES identity.member (id) ON DELETE RESTRICT;

ALTER TABLE item DROP CONSTRAINT item_created_by_fkey;
ALTER TABLE item ADD CONSTRAINT item_created_by_fkey
    FOREIGN KEY (created_by) REFERENCES identity.member (id) ON DELETE RESTRICT;
ALTER TABLE item DROP CONSTRAINT item_held_by_fkey;
ALTER TABLE item ADD CONSTRAINT item_held_by_fkey
    FOREIGN KEY (held_by) REFERENCES identity.member (id) ON DELETE RESTRICT;

ALTER TABLE bin DROP CONSTRAINT bin_created_by_fkey;
ALTER TABLE bin ADD CONSTRAINT bin_created_by_fkey
    FOREIGN KEY (created_by) REFERENCES identity.member (id) ON DELETE RESTRICT;
ALTER TABLE bin DROP CONSTRAINT bin_owner_id_fkey;
ALTER TABLE bin ADD CONSTRAINT bin_owner_id_fkey
    FOREIGN KEY (owner_id) REFERENCES identity.member (id) ON DELETE RESTRICT;

ALTER TABLE location DROP CONSTRAINT location_created_by_fkey;
ALTER TABLE location ADD CONSTRAINT location_created_by_fkey
    FOREIGN KEY (created_by) REFERENCES identity.member (id) ON DELETE RESTRICT;

ALTER TABLE item DROP CONSTRAINT item_current_bin_id_fkey;
ALTER TABLE item ADD CONSTRAINT item_current_bin_id_fkey
    FOREIGN KEY (current_bin_id) REFERENCES bin (id) ON DELETE RESTRICT;

ALTER TABLE bin DROP CONSTRAINT bin_location_id_fkey;
ALTER TABLE bin ADD CONSTRAINT bin_location_id_fkey
    FOREIGN KEY (location_id) REFERENCES location (id) ON DELETE RESTRICT;

ALTER TABLE location DROP CONSTRAINT location_parent_id_fkey;
ALTER TABLE location ADD CONSTRAINT location_parent_id_fkey
    FOREIGN KEY (parent_id) REFERENCES location (id) ON DELETE RESTRICT;

ALTER TABLE bin DROP CONSTRAINT bin_code_uniq;
ALTER TABLE bin ADD CONSTRAINT bin_code_uniq UNIQUE (code);

ALTER TABLE item     DROP CONSTRAINT item_household_id_id_uniq;
ALTER TABLE bin      DROP CONSTRAINT bin_household_id_id_uniq;
ALTER TABLE location DROP CONSTRAINT location_household_id_id_uniq;

ALTER TABLE photo          DROP COLUMN household_id;
ALTER TABLE return_request DROP COLUMN household_id;
ALTER TABLE item_event     DROP COLUMN household_id;
ALTER TABLE item           DROP COLUMN household_id;
ALTER TABLE bin            DROP COLUMN household_id;
ALTER TABLE location       DROP COLUMN household_id;
