-- +goose Up
-- Photo storage (NSTR-34): the media bounded context's first migration,
-- introducing photo (one row per stored image) and item_photo (the
-- ordered, at-most-one-primary junction attaching photos to storage
-- items).
--
-- id has no DEFAULT: the app supplies a UUIDv7 (domain.NewPhotoID),
-- matching item/bin rather than location/app_user's gen_random_uuid()
-- default, for B-tree index locality on inserts.
--
-- storage_ref is the PhotoStore key (items/<item>/<hash>.<ext>, see
-- internal/media/adapter/photo_validation.go's buildStorageKey) and is the
-- dedup key: because the key layout encodes both the owning item and the
-- content hash, storage_ref alone is already item-scoped, so the same
-- bytes on two different items are two legitimate, non-colliding rows.
-- content_sha256 is therefore a plain (non-unique) index only — a
-- server-verified fact about the bytes, not a cross-item dedup key.
--
-- size_bytes/content_type are the other server-verified upload facts
-- (PhotoService.Upload computes all three from the validated/staged
-- bytes, never from a caller-supplied claim).
--
-- storage_backend defaults to 'local' — NSTR-35's mixed-state
-- requirement: rows written before an operator switches
-- MEDIA_STORAGE_BACKEND must stay readable afterward, which requires
-- every row to carry its OWN write backend rather than trusting whatever
-- the deployment currently writes new photos to (Nestova's NES-132
-- retrofitted the identical column for the identical reason).
--
-- thumb_storage_ref is deliberately part of this FIRST version of the
-- migration, not a later ALTER: NSTR-84 (thumbnail generation) populates
-- it, treating a thumbnail failure as non-fatal (a NULL reference falls
-- back to serving the full image), so it is nullable with no UNIQUE
-- constraint — two rows may legitimately hold NULL at once. This ticket
-- owns the column's SCHEMA only; no Go code in NSTR-34 reads or writes
-- it.
--
-- uploaded_by references app_user (id) ON DELETE RESTRICT, mirroring
-- item.created_by/held_by's identical rationale (00008_item.sql):
-- identity soft-deletes users and never hard-deletes them, so this is a
-- formality, not a real operational constraint.
--
-- Blank/positive checks use length(btrim(x)) > 0 / x > 0, matching the
-- SonarCloud plsql:NullComparison false-positive workaround documented in
-- 00004_device_token.sql (never x <> '').
CREATE TABLE photo (
    id                uuid        PRIMARY KEY,
    storage_ref       text        NOT NULL,
    content_sha256    text        NOT NULL,
    size_bytes        bigint      NOT NULL,
    content_type      text        NOT NULL,
    storage_backend   text        NOT NULL DEFAULT 'local',
    thumb_storage_ref text,
    uploaded_by       uuid        NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT photo_storage_ref_check CHECK (length(btrim(storage_ref)) > 0),
    CONSTRAINT photo_content_sha256_check CHECK (length(btrim(content_sha256)) > 0),
    CONSTRAINT photo_size_bytes_check CHECK (size_bytes > 0),
    CONSTRAINT photo_content_type_check CHECK (length(btrim(content_type)) > 0),
    CONSTRAINT photo_storage_ref_uniq UNIQUE (storage_ref),
    CONSTRAINT photo_uploaded_by_fkey FOREIGN KEY (uploaded_by) REFERENCES app_user (id) ON DELETE RESTRICT
);

-- Backs FindByStorageRef's dedup probe alongside the UNIQUE constraint
-- above (which already indexes storage_ref); this is content_sha256's own,
-- deliberately non-unique, lookup index — see the table comment.
CREATE INDEX photo_content_sha256_idx ON photo (content_sha256);

-- item_photo attaches a photo to the item it was uploaded for, ordered
-- (position) with at most one primary. item_id ON DELETE CASCADE:
-- removing an item removes its photo attachments (not the photo rows
-- themselves, which PhotoService.Delete removes explicitly at the
-- application layer). photo_id ON DELETE RESTRICT is the
-- dedup-dissolves-refcounting guard PhotoService.Delete relies on
-- (item-scoped dedup means one photo row is attached to exactly one item
-- in practice): were a second junction row ever to exist, it blocks a
-- photo delete rather than silently orphaning another item's bytes.
CREATE TABLE item_photo (
    item_id    uuid    NOT NULL,
    photo_id   uuid    NOT NULL,
    position   integer NOT NULL,
    is_primary boolean NOT NULL DEFAULT false,

    PRIMARY KEY (item_id, photo_id),
    CONSTRAINT item_photo_position_check CHECK (position >= 0),
    CONSTRAINT item_photo_item_id_fkey FOREIGN KEY (item_id) REFERENCES item (id) ON DELETE CASCADE,
    CONSTRAINT item_photo_photo_id_fkey FOREIGN KEY (photo_id) REFERENCES photo (id) ON DELETE RESTRICT,
    CONSTRAINT item_photo_position_uniq UNIQUE (item_id, position)
);

-- At most one primary photo per item — the same partial-unique-index
-- pattern api_key_current_uniq (00005_api_key.sql) already establishes for
-- "at most one row of a kind."
CREATE UNIQUE INDEX item_photo_primary_uniq ON item_photo (item_id) WHERE is_primary;

-- +goose Down
DROP INDEX IF EXISTS item_photo_primary_uniq;
DROP TABLE IF EXISTS item_photo;
DROP INDEX IF EXISTS photo_content_sha256_idx;
DROP TABLE IF EXISTS photo;
