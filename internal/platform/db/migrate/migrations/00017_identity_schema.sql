-- +goose Up
-- NSTR-116: Nestorage stops owning identity and adopts the shared identity
-- schema (epic NSTR-112). Interim note: nestcore's own identity schema
-- migration (NSTR-120) and Go package (NSTR-121) are not built yet, so this
-- migration creates the `identity` schema itself, modeled closely on
-- Nestova's baseline household/member tables (see Nestova's 00001_baseline.sql
-- and 00002_auth.sql) — but NOT an exact match, and the divergences are
-- deliberate for now, not merely unfinished: email/password_hash are NOT NULL
-- here (Nestova's are nullable, paired with a member_credentials_complete
-- CHECK, since it supports members with no login — Nestorage does not yet);
-- color lives on Nestorage's own profile table below rather than a shared
-- member.color_key; active is added here (NSTR-120 will add its own copy to
-- Nestova's member); and Nestova's member_household_name_uniq /
-- member_household_id_id_uniq indexes have no counterpart here. Which side
-- wins is nestcore's call to make when NSTR-120 actually lands. IF NOT EXISTS
-- guards below let that later, nestcore-owned migration adopt this schema
-- without conflict.
--
-- Scope decision (Eric, 2026-07-29): neither app is live, so there is no
-- app_user data to migrate — this migration drops and recreates rather than
-- transforming rows. This also means the Up direction below is only safe
-- against an empty (freshly reset) database: every re-pointed FK validates
-- against identity.member, which is empty at that point in the migration, so
-- a database already holding rows in any of the 13 re-pointed tables fails
-- the corresponding ADD CONSTRAINT with SQLSTATE 23503 — recoverable by hand
-- for twelve of them, but not for item_event (see its own NOT VALID note
-- below).
CREATE SCHEMA IF NOT EXISTS identity;

CREATE TABLE IF NOT EXISTS identity.household (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- role is the unified owner/adult/child vocabulary (NSTR-112): apps derive
-- admin-versus-member from this, never the reverse. active is IDENTITY-level
-- (Eric, 2026-07-29) — deactivation blocks login in both apps, so it lives
-- here rather than on Nestorage's own profile table below.
CREATE TABLE IF NOT EXISTS identity.member (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id  uuid        NOT NULL REFERENCES identity.household (id) ON DELETE CASCADE,
    display_name  text        NOT NULL,
    email         citext      NOT NULL,
    password_hash text        NOT NULL,
    role          text        NOT NULL CHECK (role IN ('owner', 'adult', 'child')),
    active        boolean     NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    -- Named to match Nestova's own member_email_unique (its 00002_auth.sql)
    -- so isDuplicateEmail (internal/identity/adapter/postgres.go) keeps
    -- matching once nestcore owns this table.
    CONSTRAINT member_email_unique UNIQUE (email)
);
CREATE INDEX IF NOT EXISTS identity_member_household_id_idx ON identity.member (household_id);
CREATE INDEX IF NOT EXISTS identity_member_active_idx ON identity.member (active) WHERE active;

-- Server-side session store, moved from the app schema's own `sessions`
-- (00003_sessions.sql) into the shared identity schema — matches the exact
-- layout github.com/alexedwards/scs/pgxstore requires; Nestorage's own
-- session store (internal/platform/session) targets this table explicitly by
-- schema-qualified name rather than relying on search_path.
CREATE TABLE IF NOT EXISTS identity.sessions (
    token  text        PRIMARY KEY,
    data   bytea       NOT NULL,
    expiry timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS identity_sessions_expiry_idx ON identity.sessions (expiry);

-- Nestorage's owner palette stays app-side (presentation, not identity),
-- keyed one-to-one on identity.member. Deliberately carries color ONLY — no
-- active column, which lives on identity.member above.
CREATE TABLE profile (
    member_id uuid PRIMARY KEY REFERENCES identity.member (id) ON DELETE CASCADE,
    color     text NOT NULL CHECK (color IN ('indigo', 'steel', 'teal', 'peri'))
);

-- Re-point every app_user FK to identity.member (13 sites). Constraint names
-- are unchanged so no adapter code depends on a renamed constraint.
ALTER TABLE device_token
    DROP CONSTRAINT device_token_user_id_fkey,
    ADD CONSTRAINT device_token_user_id_fkey FOREIGN KEY (user_id) REFERENCES identity.member (id) ON DELETE CASCADE;

ALTER TABLE location
    DROP CONSTRAINT location_created_by_fkey,
    ADD CONSTRAINT location_created_by_fkey FOREIGN KEY (created_by) REFERENCES identity.member (id) ON DELETE RESTRICT;

ALTER TABLE bin
    DROP CONSTRAINT bin_owner_id_fkey,
    ADD CONSTRAINT bin_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES identity.member (id) ON DELETE RESTRICT,
    DROP CONSTRAINT bin_created_by_fkey,
    ADD CONSTRAINT bin_created_by_fkey FOREIGN KEY (created_by) REFERENCES identity.member (id) ON DELETE RESTRICT;

ALTER TABLE item
    DROP CONSTRAINT item_held_by_fkey,
    ADD CONSTRAINT item_held_by_fkey FOREIGN KEY (held_by) REFERENCES identity.member (id) ON DELETE RESTRICT,
    DROP CONSTRAINT item_created_by_fkey,
    ADD CONSTRAINT item_created_by_fkey FOREIGN KEY (created_by) REFERENCES identity.member (id) ON DELETE RESTRICT;

ALTER TABLE photo
    DROP CONSTRAINT photo_uploaded_by_fkey,
    ADD CONSTRAINT photo_uploaded_by_fkey FOREIGN KEY (uploaded_by) REFERENCES identity.member (id) ON DELETE RESTRICT;

-- NOT VALID for the same reason the Down half below uses it: item_event is
-- append-only (00012_item_event.sql revokes UPDATE/DELETE/TRUNCATE with a
-- trigger backstop), so pre-existing rows can neither be re-pointed nor
-- cleared, and identity.member is empty at this point in the migration. The
-- other twelve re-pointed tables above hit the same empty-target problem on
-- a non-empty database, but stay recoverable by hand (see the header note).
ALTER TABLE item_event
    DROP CONSTRAINT item_event_actor_user_id_fkey,
    ADD CONSTRAINT item_event_actor_user_id_fkey FOREIGN KEY (actor_user_id) REFERENCES identity.member (id) ON DELETE RESTRICT NOT VALID;

ALTER TABLE return_request
    DROP CONSTRAINT return_request_requester_id_fkey,
    ADD CONSTRAINT return_request_requester_id_fkey FOREIGN KEY (requester_id) REFERENCES identity.member (id) ON DELETE RESTRICT,
    DROP CONSTRAINT return_request_holder_id_fkey,
    ADD CONSTRAINT return_request_holder_id_fkey FOREIGN KEY (holder_id) REFERENCES identity.member (id) ON DELETE RESTRICT;

ALTER TABLE notification
    DROP CONSTRAINT notification_recipient_id_fkey,
    ADD CONSTRAINT notification_recipient_id_fkey FOREIGN KEY (recipient_id) REFERENCES identity.member (id) ON DELETE CASCADE;

ALTER TABLE notification_preference
    DROP CONSTRAINT notification_preference_user_id_fkey,
    ADD CONSTRAINT notification_preference_user_id_fkey FOREIGN KEY (user_id) REFERENCES identity.member (id) ON DELETE CASCADE;

ALTER TABLE label_preference
    DROP CONSTRAINT label_preference_user_id_fkey,
    ADD CONSTRAINT label_preference_user_id_fkey FOREIGN KEY (user_id) REFERENCES identity.member (id) ON DELETE CASCADE;

-- No row copies: no data to carry (scope decision above).
DROP INDEX IF EXISTS app_user_active_idx;
DROP TABLE app_user;

DROP INDEX IF EXISTS sessions_expiry_idx;
DROP TABLE sessions;

-- +goose Down
CREATE TABLE sessions (
    token  text        PRIMARY KEY,
    data   bytea       NOT NULL,
    expiry timestamptz NOT NULL
);
CREATE INDEX sessions_expiry_idx ON sessions (expiry);

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
    CONSTRAINT app_user_email_unique UNIQUE (email)
);
CREATE INDEX app_user_active_idx ON app_user (active) WHERE active;

-- Re-pointing each FK below back to the freshly (empty) recreated app_user
-- validates every existing row against it, which would fail for any row
-- seeded since Up ran. There is nothing to preserve across this rollback (no
-- data-migration step exists in either direction, per the scope decision
-- above), so the simplest correct fix is to clear the referencing rows
-- first — CASCADE also clears anything transitively referencing them (e.g.
-- item_link, item_photo). item_event is deliberately excluded: it is
-- append-only (00012_item_event.sql revokes UPDATE/DELETE/TRUNCATE from
-- every role, including the table owner, with a trigger backstop), so its
-- own FK re-add below uses NOT VALID instead of clearing it.
TRUNCATE TABLE device_token, location, bin, item, photo,
    return_request, notification, notification_preference, label_preference
    RESTART IDENTITY CASCADE;

ALTER TABLE label_preference
    DROP CONSTRAINT label_preference_user_id_fkey,
    ADD CONSTRAINT label_preference_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user (id) ON DELETE CASCADE;

ALTER TABLE notification_preference
    DROP CONSTRAINT notification_preference_user_id_fkey,
    ADD CONSTRAINT notification_preference_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user (id) ON DELETE CASCADE;

ALTER TABLE notification
    DROP CONSTRAINT notification_recipient_id_fkey,
    ADD CONSTRAINT notification_recipient_id_fkey FOREIGN KEY (recipient_id) REFERENCES app_user (id) ON DELETE CASCADE;

ALTER TABLE return_request
    DROP CONSTRAINT return_request_holder_id_fkey,
    ADD CONSTRAINT return_request_holder_id_fkey FOREIGN KEY (holder_id) REFERENCES app_user (id) ON DELETE RESTRICT,
    DROP CONSTRAINT return_request_requester_id_fkey,
    ADD CONSTRAINT return_request_requester_id_fkey FOREIGN KEY (requester_id) REFERENCES app_user (id) ON DELETE RESTRICT;

-- NOT VALID: item_event cannot be truncated or have rows deleted (see the
-- TRUNCATE comment above), so this skips validating existing rows against
-- the new target — acceptable because nothing is live yet (no production
-- item_event rows exist anywhere) and this path only runs for test resets.
ALTER TABLE item_event
    DROP CONSTRAINT item_event_actor_user_id_fkey,
    ADD CONSTRAINT item_event_actor_user_id_fkey FOREIGN KEY (actor_user_id) REFERENCES app_user (id) ON DELETE RESTRICT NOT VALID;

ALTER TABLE photo
    DROP CONSTRAINT photo_uploaded_by_fkey,
    ADD CONSTRAINT photo_uploaded_by_fkey FOREIGN KEY (uploaded_by) REFERENCES app_user (id) ON DELETE RESTRICT;

ALTER TABLE item
    DROP CONSTRAINT item_created_by_fkey,
    ADD CONSTRAINT item_created_by_fkey FOREIGN KEY (created_by) REFERENCES app_user (id) ON DELETE RESTRICT,
    DROP CONSTRAINT item_held_by_fkey,
    ADD CONSTRAINT item_held_by_fkey FOREIGN KEY (held_by) REFERENCES app_user (id) ON DELETE RESTRICT;

ALTER TABLE bin
    DROP CONSTRAINT bin_created_by_fkey,
    ADD CONSTRAINT bin_created_by_fkey FOREIGN KEY (created_by) REFERENCES app_user (id) ON DELETE RESTRICT,
    DROP CONSTRAINT bin_owner_id_fkey,
    ADD CONSTRAINT bin_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES app_user (id) ON DELETE RESTRICT;

ALTER TABLE location
    DROP CONSTRAINT location_created_by_fkey,
    ADD CONSTRAINT location_created_by_fkey FOREIGN KEY (created_by) REFERENCES app_user (id) ON DELETE RESTRICT;

ALTER TABLE device_token
    DROP CONSTRAINT device_token_user_id_fkey,
    ADD CONSTRAINT device_token_user_id_fkey FOREIGN KEY (user_id) REFERENCES app_user (id) ON DELETE CASCADE;

DROP TABLE profile;
DROP TABLE identity.sessions;
DROP TABLE identity.member;
DROP TABLE identity.household;
DROP SCHEMA identity;
