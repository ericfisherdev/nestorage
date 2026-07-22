-- +goose Up
-- citext is a trusted extension in Postgres 17, so it installs without
-- superuser. It makes email comparison case-insensitive without a lower()
-- call while leaving LIKE usable for NSTR-21's user search. A nondeterministic
-- ICU collation was considered and rejected: Postgres does not support
-- pattern matching against nondeterministic collations, and B-tree
-- deduplication is disabled under them.
CREATE EXTENSION IF NOT EXISTS citext;

-- The table is app_user, not user: user is a reserved key word in Postgres 17
-- and CREATE TABLE user (...) is a syntax error, verified against the compose
-- service running 17.10. Every later FK (including NSTR-22's device_token)
-- targets app_user (id).
CREATE TABLE app_user (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name  text        NOT NULL,
    email         citext      NOT NULL,
    password_hash text        NOT NULL,
    role          text        NOT NULL CHECK (role IN ('admin', 'member')),
    color         text        NOT NULL CHECK (color IN ('indigo', 'steel', 'teal', 'peri')),
    active        boolean     NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    -- Named explicitly rather than left to the auto-generated name, so the
    -- adapter can match on pgconn.PgError.ConstraintName instead of parsing
    -- an error message. This is the constraint that satisfies the
    -- case-insensitivity criterion (citext compares email case-insensitively).
    CONSTRAINT app_user_email_unique UNIQUE (email)
);

-- Backs the active-user listing.
CREATE INDEX app_user_active_idx ON app_user (active) WHERE active;

-- +goose Down
DROP INDEX IF EXISTS app_user_active_idx;
DROP TABLE IF EXISTS app_user;

-- Intentionally NOT dropping the citext extension: other objects may come to
-- depend on it, and dropping extensions needs elevated privileges in many
-- environments (matching the rationale in Nestova's 00002_auth.sql).
