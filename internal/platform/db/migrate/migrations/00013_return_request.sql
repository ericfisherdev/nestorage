-- +goose Up
-- Return requests (NSTR-43): anyone who can see a checked-out item can ask
-- for it back. A request has exactly three states — open, fulfilled,
-- cancelled — both fulfilled and cancelled terminal. This table is
-- operational state owned by the item aggregate (item_id ON DELETE CASCADE,
-- matching item_link/item_photo), unlike item_event's own audit trail,
-- which deliberately carries no foreign key at all so it outlives the row
-- it describes (see 00012_item_event.sql's own comment) — a return request
-- has no reason to outlive the item it is about.
--
-- id has no DEFAULT: the app supplies a UUIDv7 (domain.NewReturnRequestID),
-- matching every other aggregate id in this schema for B-tree index
-- locality on inserts.
--
-- holder_id snapshots WHO held the item at request time. It is not
-- re-derived from item.held_by at read time: item.held_by can only change
-- via a placement operation, and every placement operation either leaves
-- the item held by the same person or resolves every open request on it
-- (fulfilment, NSTR-43's own OperationService.transition hook), so the
-- snapshot never goes stale while a request stays open.
--
-- message is the requester's own optional note — distinct from
-- item_event.note, the free-text field on a check-out/return operation
-- (NSTR-43's reconciliation R16: neither is copied into the other).
--
-- resolved_at is NULL for an open request, set the instant status leaves
-- open — return_request_resolved_check is the database mirror of
-- ReturnRequest.Fulfill/Cancel's own "unmodified on failure, both fields set
-- together on success" contract.
--
-- Blank checks use length(btrim(x)) > 0, never x <> '', per the SonarCloud
-- plsql:NullComparison false-positive workaround documented in
-- 00004_device_token.sql.
CREATE TABLE return_request (
    id            uuid        PRIMARY KEY,           -- app-supplied UUIDv7
    item_id       uuid        NOT NULL,
    requester_id  uuid        NOT NULL,
    holder_id     uuid        NOT NULL,              -- holder at request time
    message       text,                              -- optional requester note
    status        text        NOT NULL DEFAULT 'open',
    created_at    timestamptz NOT NULL DEFAULT now(),
    resolved_at   timestamptz,
    CONSTRAINT return_request_status_check   CHECK (status IN ('open','fulfilled','cancelled')),
    CONSTRAINT return_request_resolved_check CHECK ((status = 'open') = (resolved_at IS NULL)),
    CONSTRAINT return_request_message_check  CHECK (message IS NULL OR length(btrim(message)) > 0),
    CONSTRAINT return_request_item_id_fkey      FOREIGN KEY (item_id)      REFERENCES item (id)     ON DELETE CASCADE,
    CONSTRAINT return_request_requester_id_fkey FOREIGN KEY (requester_id) REFERENCES app_user (id) ON DELETE RESTRICT,
    CONSTRAINT return_request_holder_id_fkey    FOREIGN KEY (holder_id)    REFERENCES app_user (id) ON DELETE RESTRICT
);

-- The DB-level duplicate-open-request guard: uniqueness enforced only among
-- rows matching the predicate (verified against the PostgreSQL 17 partial
-- index docs), so a requester can freely re-request the same item after an
-- earlier request of theirs was fulfilled or cancelled. The adapter maps
-- this constraint's 23505 via pgconn.PgError.ConstraintName, the same
-- pattern bin_code_uniq's own violation is mapped with.
CREATE UNIQUE INDEX return_request_open_uniq ON return_request (item_id, requester_id) WHERE status = 'open';

-- Backs FulfillOpenForItem's per-item open-request read/update — a partial
-- index for the same reason item_event_bin_occurred_idx is one: only open
-- rows are ever selected by item_id, so a full-table index would waste
-- space on rows this query never touches.
CREATE INDEX return_request_item_open_idx ON return_request (item_id) WHERE status = 'open';

-- +goose Down
DROP INDEX IF EXISTS return_request_item_open_idx;
DROP INDEX IF EXISTS return_request_open_uniq;
DROP TABLE IF EXISTS return_request;
