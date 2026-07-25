-- +goose Up
-- Per-user remembered label size (NSTR-51): the size a household member
-- last printed with, so their next visit to the print screen preselects it
-- instead of the registry default. Stored in Postgres, not the session or a
-- cookie: a session expires on its own hard lifetime, and the entryway
-- kiosk's cookie jar is shared across whoever is standing in front of it, so
-- either would leak one member's choice to the next. Mirrors
-- notification_preference's own shape (00015_notification_preference.sql).
--
-- size_id is free text, not a foreign key into anything: internal/labels/
-- domain.Registry is an in-process catalogue with no table of its own (see
-- its own doc), so there is nothing here to reference. A stored id that no
-- longer names a registered size (e.g. a removed builtin) is handled by the
-- reading side falling back to the registry's own default, not by a
-- constraint here.
--
-- The primary key IS the natural upsert target: SizePreferenceRepository.Upsert
-- always writes INSERT ... ON CONFLICT (user_id) DO UPDATE, never a
-- read-modify-write.
--
-- ON DELETE CASCADE mirrors device_token's own rationale (00004_device_token.sql):
-- a preference row has no reason to outlive the user it belongs to.
--
-- The blank-rejecting CHECK uses length(btrim(x)) > 0 rather than the
-- btrim(x) <> '' idiom, for the identical SonarCloud plsql:NullComparison
-- reason documented on device_token's own token_hash/name checks.
CREATE TABLE label_preference (
    user_id    uuid        NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    size_id    text        NOT NULL CHECK (length(btrim(size_id)) > 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id)
);

-- +goose Down
DROP TABLE IF EXISTS label_preference;
