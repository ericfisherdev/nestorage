-- +goose Up
-- Per-device API tokens (NSTR-22): the native Android client logs in as a
-- real user and receives a long-lived bearer token scoped to one device,
-- rather than carrying the account-wide API key NSTR-23 issues — so item
-- history stays attributable to the person who acted, and a lost phone is a
-- one-token revocation instead of an account-wide credential rotation.
--
-- This ports the shape Nestova's kiosk_device already proved out (see
-- 00026_kiosk_device.sql there), with two deliberate differences: the owner
-- is a user, not a household, and many active tokens per user are allowed —
-- a person may carry more than one device — so unlike the kiosk (at most one
-- active device per household) there is no partial-unique index limiting the
-- active count.
--
-- token_hash is SHA-256, not the argon2id internal/platform/crypto applies
-- to passwords: the token is 256 bits of crypto/rand output, not a
-- human-chosen secret, so it already carries full entropy and there is no
-- dictionary/rainbow-table attack for a KDF to defend against — paying
-- argon2's deliberate CPU/memory cost on every Android request would be a
-- self-inflicted denial of service.
--
-- revoked_at (nullable) marks a token invalidated by an explicit revoke or a
-- user deactivation (NSTR-21's DeleteAllForUser); the row is kept, not
-- deleted, as a record of which devices a user was ever issued a token for.
CREATE TABLE device_token (
    id            uuid        PRIMARY KEY,
    user_id       uuid        NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    token_hash    text        NOT NULL CHECK (btrim(token_hash) <> ''),
    name          text        NOT NULL CHECK (btrim(name) <> ''),
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz,
    revoked_at    timestamptz,
    -- Named explicitly (matching app_user_email_unique's rationale) so the
    -- adapter can match on pgconn.PgError.ConstraintName. This constraint is
    -- serving double duty: it is also the per-request authentication lookup
    -- index (GetByTokenHash), so do not drop it thinking it is only a guard,
    -- and do not add a redundant index alongside it.
    CONSTRAINT device_token_token_hash_uniq UNIQUE (token_hash)
);

-- Supports DeviceTokenRepository.ListByUser: the self-service device list,
-- newest first.
CREATE INDEX device_token_user_idx ON device_token (user_id, created_at DESC);

-- Deliberately NOT unique on (user_id, name): re-pairing a phone under the
-- same name a previous device used is a normal flow, not corruption.

-- +goose Down
DROP INDEX IF EXISTS device_token_user_idx;
DROP TABLE IF EXISTS device_token;
