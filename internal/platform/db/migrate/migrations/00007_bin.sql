-- +goose Up
-- Bins (NSTR-27): a bin belongs to a location, carries a printed label code,
-- and defaults to public visibility, with private-to-creator as the other
-- option. Bins are the second aggregate in the same internal/storage
-- bounded context 00006_locations.sql established; this migration must run
-- after it because location_id is a foreign key into that table.
--
-- id has no DEFAULT: the app supplies a UUIDv7 (domain.NewBinID), matching
-- device_token and api_key rather than location/app_user's
-- gen_random_uuid() default, for B-tree index locality on inserts.
--
-- code is stored normalized (trimmed, upper-cased — see
-- domain.NormalizeBinCode) so a scanned label and a typed one always match
-- the same row; Space Mono renders it in the UI. bin_code_uniq doubles as
-- the lookup index for the /b/CODE page (BinRepository.FindVisibleByCode).
--
-- owner_id is nullable from day one: null means the shared/Family bin, a
-- set value is the household member whose color the bin wears in the
-- browse UI. The column exists now even though owner filtering ships with
-- NSTR-31, so no later migration runs against live data — the same
-- rationale 00006_locations.sql applies to location.parent_id.
--
-- location_id, owner_id, and created_by are all ON DELETE RESTRICT: a
-- location or user referenced by a bin cannot be removed out from under it.
-- location_id's RESTRICT is also what makes LocationRepository.Delete's
-- ErrLocationNotEmpty guard (00006_locations.sql) cover bins, not only
-- child locations — isForeignKeyViolation in storage/adapter matches on
-- SQLSTATE alone, so it already treats any referrer, bins included, as
-- "not empty".
--
-- All three foreign keys and the code uniqueness are named explicitly
-- (matching app_user_email_unique's and device_token_token_hash_uniq's
-- rationale) so BinRepository.Create can distinguish, via
-- pgconn.PgError.ConstraintName, a duplicate code from an unknown location
-- from an unknown owner/creator — three differently-shaped foreign keys,
-- unlike device_token's single one.
--
-- Blank checks use length(btrim(x)) > 0, never x <> '' — the SonarCloud
-- plsql:NullComparison false positive documented in 00004_device_token.sql.
CREATE TABLE bin (
    id          uuid        PRIMARY KEY,
    code        text        NOT NULL,
    name        text        NOT NULL CHECK (length(btrim(name)) > 0),
    description text        NOT NULL DEFAULT '',
    location_id uuid        NOT NULL,
    owner_id    uuid,
    created_by  uuid        NOT NULL,
    visibility  text        NOT NULL DEFAULT 'public' CHECK (visibility IN ('public', 'private')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT bin_code_uniq UNIQUE (code),
    CONSTRAINT bin_location_id_fkey FOREIGN KEY (location_id) REFERENCES location (id) ON DELETE RESTRICT,
    CONSTRAINT bin_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES app_user (id) ON DELETE RESTRICT,
    CONSTRAINT bin_created_by_fkey FOREIGN KEY (created_by) REFERENCES app_user (id) ON DELETE RESTRICT
);

-- Back the browse-UI filters NSTR-31 adds: by location, and by owner.
CREATE INDEX bin_location_idx ON bin (location_id);
CREATE INDEX bin_owner_idx ON bin (owner_id);

-- +goose Down
DROP INDEX IF EXISTS bin_owner_idx;
DROP INDEX IF EXISTS bin_location_idx;
DROP TABLE IF EXISTS bin;
