-- +goose Up
-- Notification preferences (NSTR-45): per-user, per-event-type, per-channel
-- opt-in. Rows are sparse — the absence of a row means the default, never
-- an error — so in practice only email rows are ever written: in_app is
-- structurally always-on (see internal/notify/domain.PreferenceReader's own
-- doc) and is never stored here. The channel column stays generic rather
-- than being narrowed to an email-only enum, so a future push channel is a
-- data change, not a schema change.
--
-- The composite primary key IS the natural upsert target: SetEmailEnabled
-- always writes INSERT ... ON CONFLICT (user_id, event_type, channel) DO
-- UPDATE, never a read-modify-write.
--
-- ON DELETE CASCADE mirrors device_token's own rationale (00004_device_token.sql):
-- a preference row has no reason to outlive the user it belongs to.
--
-- The blank-rejecting CHECKs use length(btrim(x)) > 0 rather than the
-- btrim(x) <> '' idiom, for the identical SonarCloud plsql:NullComparison
-- reason documented on device_token's own token_hash/name checks.
CREATE TABLE notification_preference (
    user_id    uuid        NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    event_type text        NOT NULL CHECK (length(btrim(event_type)) > 0),
    channel    text        NOT NULL CHECK (length(btrim(channel)) > 0),
    enabled    boolean     NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, event_type, channel)
);

-- +goose Down
DROP TABLE IF EXISTS notification_preference;
