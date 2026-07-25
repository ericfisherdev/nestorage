-- +goose Up
-- Notifications (NSTR-44/NSTR-89, Sprint 6): one row per delivery attempt of
-- an event to a recipient over one channel. NSTR-44 (this migration) is the
-- in-app half only — it writes exclusively channel='in_app' rows, born
-- delivered (status='sent', sent_at set, attempts=0) at INSERT time, since
-- an in-app row IS the delivery (a local Postgres insert plus a
-- server-rendered inbox, no dispatch step). NSTR-89 (the email half) reads
-- and writes channel='email' rows through the SAME table.
--
-- CONDITION A (Sprint 6 decision, binding): this migration carries the
-- email-oriented columns — status, attempts, next_attempt_at, sent_at — and
-- the pending partial index below, even though nothing in NSTR-44 ever
-- writes a 'pending' row. This is not tidiness: nestcore's migration runner
-- calls goose.NewProvider with no options, and goose 3.27.1 defaults
-- WithAllowOutofOrder to false, which errors on a migration whose version
-- sorts below the highest already applied. Keeping every column NSTR-89
-- needs here means it ships no migration of its own and never has to claim
-- a number after NSTR-45's 00015 or leave a gap to fill in later.
--
-- id has no DEFAULT: the app supplies a UUIDv7 (domain.NewNotificationID),
-- matching every other aggregate id in this schema for B-tree index
-- locality on inserts.
--
-- recipient_id is ON DELETE CASCADE, not RESTRICT like return_request's own
-- requester_id/holder_id: a notification is entirely owned by the inbox it
-- belongs to (nothing else references it), the same rationale
-- device_token.user_id already uses (00004_device_token.sql) — a
-- notification has no reason to outlive the recipient it was written for.
--
-- source_type/source_id optionally identify the domain entity that raised
-- the notification (e.g. 'return_request' + a return_request.id) so a
-- future UI could deep-link back to it; NSTR-44 populates both for every row
-- it writes, but neither is a foreign key — the notification must survive
-- the source row's own lifecycle (a return request can itself be deleted
-- via its item's cascade), mirroring item_event's identical "no FK on
-- purpose" rationale (00012_item_event.sql).
--
-- Blank checks use length(btrim(x)) > 0, never x <> '', per the SonarCloud
-- plsql:NullComparison false-positive workaround documented in
-- 00004_device_token.sql.
CREATE TABLE notification (
    id              uuid        PRIMARY KEY,           -- app-supplied UUIDv7
    recipient_id    uuid        NOT NULL,
    channel         text        NOT NULL,
    event_type      text        NOT NULL,
    title           text        NOT NULL,
    body            text        NOT NULL,
    status          text        NOT NULL DEFAULT 'pending',
    attempts        int         NOT NULL DEFAULT 0,
    next_attempt_at timestamptz,
    sent_at         timestamptz,
    read_at         timestamptz,
    source_type     text,
    source_id       uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT notification_channel_check    CHECK (channel IN ('in_app','email')),
    CONSTRAINT notification_status_check     CHECK (status IN ('pending','sent','failed')),
    CONSTRAINT notification_event_type_check CHECK (event_type IN ('return_requested','item_returned')),
    CONSTRAINT notification_title_check      CHECK (length(btrim(title)) > 0),
    CONSTRAINT notification_body_check       CHECK (length(btrim(body)) > 0),
    CONSTRAINT notification_attempts_check   CHECK (attempts >= 0),
    CONSTRAINT notification_recipient_id_fkey FOREIGN KEY (recipient_id) REFERENCES app_user (id) ON DELETE CASCADE
);

-- Backs the inbox list and unread-badge count: both read by recipient_id,
-- ordered/filtered by read_at (NULL = unread) — NSTR-44's own InboxService.
CREATE INDEX notification_recipient_read_idx ON notification (recipient_id, read_at);

-- Backs the (future, NSTR-89) dispatcher's outbox claim — a partial index
-- for the same reason return_request_item_open_idx is one (00013_return_request.sql):
-- only pending rows are ever selected by this predicate, so a full-table
-- index would waste space on the 'sent'/'failed' rows that dominate it.
-- Stays empty until NSTR-89 lands (see this file's own top comment).
CREATE INDEX notification_pending_dispatch_idx ON notification (status, next_attempt_at) WHERE status = 'pending';

-- +goose Down
DROP INDEX IF EXISTS notification_pending_dispatch_idx;
DROP INDEX IF EXISTS notification_recipient_read_idx;
DROP TABLE IF EXISTS notification;
