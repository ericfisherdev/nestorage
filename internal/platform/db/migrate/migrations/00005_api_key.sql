-- +goose Up
-- The Nestova integration's single account-scoped api key (NSTR-23):
-- machine-to-machine calls authenticate as this one key rather than as any
-- person, so item history attributes its actions to the integration itself.
-- This is the third bearer-credential kind in the identity context —
-- session cookies (NSTR-20, human) and device tokens (NSTR-22, per-device)
-- came first — and, unlike either of those, at most one row may be current
-- at a time: the product requirement is ONE key controlling the account,
-- not many.
--
-- key_prefix is the non-secret display fragment ("ns_" plus 8 hex
-- characters) the admin screen shows to identify a row without ever
-- revealing, or letting anyone reconstruct, the secret itself.
--
-- secret_hash is SHA-256, not the argon2id internal/platform/crypto applies
-- to passwords, for the same reason recorded on device_token.token_hash
-- (see 00004_device_token.sql): this is 256 bits of crypto/rand output, not
-- a human-chosen secret, so it already carries full entropy and there is no
-- dictionary/rainbow-table attack for a KDF to defend against — paying
-- argon2's deliberate CPU/memory cost on every authenticated API request
-- would be a self-inflicted denial of service.
--
-- expires_at is set only at rotation, to the end of the chosen overlap
-- window (or to the rotation instant itself, when no overlap is chosen);
-- it stays NULL for a key that has never been superseded. revoked_at is set
-- only by an explicit revoke. The blank-rejecting CHECK below uses
-- length(btrim(label)) > 0 rather than the btrim(label) <> '' idiom
-- 00004_device_token.sql already explains: Nestorage's SonarCloud profile
-- flags <> '' as plsql:NullComparison, a dialect false positive in
-- Postgres, and length() > 0 sidesteps it without changing behavior.
CREATE TABLE api_key (
    id            uuid        PRIMARY KEY,
    key_prefix    text        NOT NULL CHECK (length(btrim(key_prefix)) > 0),
    secret_hash   text        NOT NULL,
    label         text        NOT NULL CHECK (length(btrim(label)) > 0),
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz,
    expires_at    timestamptz,
    revoked_at    timestamptz,
    -- Named explicitly (matching device_token_token_hash_uniq's rationale)
    -- so the adapter can match on pgconn.PgError.ConstraintName. This
    -- constraint is serving double duty: it is also the per-request
    -- authentication lookup index (GetBySecretHash), so do not drop it
    -- thinking it is only a guard, and do not add a redundant index
    -- alongside it.
    CONSTRAINT api_key_secret_hash_uniq UNIQUE (secret_hash)
);

-- At most one row may be "current" (never superseded, never revoked) at a
-- time — the storage-level invariant behind the product requirement that
-- ONE key controls the account. Verified empirically against Postgres
-- 16.14: a second insert while a current row exists fails with "duplicate
-- key value violates unique constraint", and setting expires_at on the
-- incumbent during rotation removes it from this index so the replacement
-- inserts cleanly — exactly the rotation sequence, enforced by the
-- database rather than by application discipline alone.
CREATE UNIQUE INDEX api_key_current_uniq ON api_key ((true))
    WHERE revoked_at IS NULL AND expires_at IS NULL;

-- Supports APIKeyRepository.ListAll: the admin screen's history view,
-- newest first.
CREATE INDEX api_key_created_at_idx ON api_key (created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS api_key_created_at_idx;
DROP INDEX IF EXISTS api_key_current_uniq;
DROP TABLE IF EXISTS api_key;
