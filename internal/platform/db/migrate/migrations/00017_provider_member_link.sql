-- +goose Up
-- NSTR-101 adds the link between a Nestova member and a Nestorage user, plus
-- the single household this install is bound to once an attach call
-- establishes it (NSTR-106). The link is a separate stable identifier, never
-- app_user.id itself: conflating them would let a re-push renumber a user
-- and orphan every created_by reference already pointing at it.
--
-- member_id and household_id are opaque, provider-controlled identifiers —
-- Nestorage never parses Nestova's own id format, so both are plain text,
-- guarded only by the length(btrim(...)) > 0 idiom 00005_api_key.sql
-- documents as the SonarCloud-safe blank check (avoiding the <> ''
-- plsql:NullComparison dialect false positive).
CREATE TABLE provider_member_link (
    id           uuid        PRIMARY KEY,
    user_id      uuid        NOT NULL REFERENCES app_user (id),
    member_id    text        NOT NULL CHECK (length(btrim(member_id)) > 0),
    household_id text        NOT NULL CHECK (length(btrim(household_id)) > 0),
    linked_at    timestamptz NOT NULL DEFAULT now(),
    -- Named explicitly (matching app_user_email_unique's own convention,
    -- 00002_identity.sql) so the adapter can match
    -- pgconn.PgError.ConstraintName instead of parsing messages. This is the
    -- constraint that makes a repeated push for the same member an update
    -- rather than a second row.
    CONSTRAINT provider_member_link_member_id_uniq UNIQUE (member_id),
    -- A user carries at most one provider identity.
    CONSTRAINT provider_member_link_user_id_uniq UNIQUE (user_id)
);

-- federation_binding holds the single Nestova household this install is
-- bound to. It lives in the database, not configuration:
-- config.ProviderConfig is env-only and validated fail-fast at startup, so a
-- value recorded by an incoming attach call could never be a config field.
-- Held to a single row by a unique index on ((true)) — the exact
-- single-row pattern 00005_api_key.sql's own api_key_current_uniq already
-- uses for the current key.
CREATE TABLE federation_binding (
    household_id text        NOT NULL CHECK (length(btrim(household_id)) > 0),
    bound_at     timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX federation_binding_single_row_uniq ON federation_binding ((true));

-- +goose Down
DROP INDEX IF EXISTS federation_binding_single_row_uniq;
DROP TABLE IF EXISTS federation_binding;
DROP TABLE IF EXISTS provider_member_link;
