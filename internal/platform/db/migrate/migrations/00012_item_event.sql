-- +goose Up
-- The item event log (NSTR-40): one append-only table recording the
-- lifecycle of every item. Corrections are new events, never edits — that
-- is what lets the log be trusted as an audit trail. NSTR-41 (writers),
-- NSTR-42 (timeline UI), and NSTR-43 (return requests) all build on this
-- table; nothing here is optional scaffolding for a later ticket to
-- rethink.
--
-- id has no DEFAULT: the app supplies a UUIDv7 (domain.NewItemEventID),
-- matching item/bin's own rationale for B-tree index locality on inserts.
--
-- item_id and bin_id deliberately carry NO foreign key to item or bin.
-- Both hard-delete (item_postgres.go's Delete, bin_postgres.go's Delete);
-- ON DELETE RESTRICT would let this audit trail block a deletion, and ON
-- DELETE CASCADE would erase it at the exact moment it matters most — a
-- 'deleted' event must survive the transaction that removes its item row.
-- item_name and bin_label snapshot the names at event time so history for a
-- deleted item or bin stays renderable, the same reasoning
-- 00008_item.sql's held_by/created_by comment gives for a deactivated user.
--
-- actor_user_id DOES carry a foreign key, unlike item_id/bin_id above: it is
-- safe because identity only ever soft-deletes a user (SetActive), never
-- hard-deletes one — the same rationale item.held_by's own FK already
-- relies on. actor_label is still a snapshot (not just a join target) so a
-- later rename does not rewrite history that already happened.
--
-- Every event stores the acting principal's resolution, so attribution
-- survives all three credentials (session, device token, account api key):
-- actor_kind is 'user' for a session or device-token principal (a real
-- person, actor_user_id set) or 'integration' for the account api key (the
-- Nestova integration, no user behind it, actor_user_id NULL) —
-- item_event_actor_check is what makes each NULL/NOT NULL pairing lawful
-- for exactly one kind and rejects any other actor_kind value outright.
--
-- bin_id/bin_label travel together (item_event_bin_check): both NULL for an
-- event with no bin context (a return-request kind, or 'edited'), both set
-- otherwise.
--
-- from_location_id/from_location_label/to_location_id/to_location_label are
-- 'moved'-only columns (item_event_move_check): all four set when
-- kind = 'moved', all four NULL otherwise. 'moved' here means the BIN
-- HOLDING THIS ITEM WAS RELOCATED TO A DIFFERENT LOCATION (emitted by
-- NSTR-41's BinMover.Move, fanned out one event per item in the bin) — bin_id/
-- bin_label carry the containing bin, unchanged by the move. It does NOT
-- mean a direct item bin-to-bin move; that is a distinct future feature that
-- will claim its own kind rather than reusing this one. No foreign key to
-- location, for the same reason bin has none: a location with no bins left
-- can be deleted.
--
-- changed_fields is 'edited'-only (item_event_edit_check): a non-empty
-- subset of {name, description, quantity} when kind = 'edited', NULL
-- otherwise. This is the first array column in the schema; the CHECK below
-- (verified against the PostgreSQL 17 array-functions docs: <@ is the
-- "is contained by" operator, array_length(x, 1) is NULL — not 0 — for an
-- empty array, which is why the check tests IS NOT NULL AND > 0 rather than
-- <> 0) is what keeps it typed rather than a free-form payload blob.
--
-- kind has NINE values: created, added, removed, returned, moved, deleted,
-- return_requested, return_request_cancelled, edited. There is
-- deliberately no return_request_fulfilled — fulfilment happens inside the
-- same transaction as a placement change that already emits added or
-- returned, so a fourth kind would double-write one physical action.
--
-- Every CHECK below is named explicitly (matching item_quantity_check's own
-- rationale in 00008_item.sql) so the adapter can distinguish them, via
-- pgconn.PgError.ConstraintName, from one another — all are SQLSTATE 23514.
-- Blank checks use length(btrim(x)) > 0, never x <> '' — the SonarCloud
-- plsql:NullComparison false positive documented in 00004_device_token.sql.
CREATE TABLE item_event (
    id                   uuid        PRIMARY KEY,
    item_id              uuid        NOT NULL,
    item_name            text        NOT NULL CHECK (length(btrim(item_name)) > 0),
    kind                 text        NOT NULL,
    actor_kind           text        NOT NULL,
    actor_user_id        uuid,
    actor_label          text        NOT NULL CHECK (length(btrim(actor_label)) > 0),
    bin_id               uuid,
    bin_label            text,
    note                 text,
    from_location_id     uuid,
    from_location_label  text,
    to_location_id       uuid,
    to_location_label    text,
    changed_fields       text[],
    occurred_at          timestamptz NOT NULL DEFAULT now(),
    created_at           timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT item_event_kind_check CHECK (kind IN (
        'created', 'added', 'removed', 'returned', 'moved', 'deleted',
        'return_requested', 'return_request_cancelled', 'edited'
    )),
    CONSTRAINT item_event_actor_check CHECK (
        (actor_kind = 'user' AND actor_user_id IS NOT NULL) OR
        (actor_kind = 'integration' AND actor_user_id IS NULL)
    ),
    CONSTRAINT item_event_actor_user_id_fkey FOREIGN KEY (actor_user_id) REFERENCES app_user (id) ON DELETE RESTRICT,
    CONSTRAINT item_event_bin_check CHECK (num_nonnulls(bin_id, bin_label) IN (0, 2)),
    CONSTRAINT item_event_move_check CHECK (
        (kind = 'moved' AND num_nonnulls(from_location_id, from_location_label, to_location_id, to_location_label) = 4) OR
        (kind <> 'moved' AND num_nonnulls(from_location_id, from_location_label, to_location_id, to_location_label) = 0)
    ),
    CONSTRAINT item_event_edit_check CHECK (
        (kind = 'edited'
             AND changed_fields IS NOT NULL
             AND array_length(changed_fields, 1) > 0
             AND changed_fields <@ ARRAY['name', 'description', 'quantity'])
        OR (kind <> 'edited' AND changed_fields IS NULL)
    )
);

-- Backs ItemEventRepository.ListByItem's newest-first keyset page: the
-- UUIDv7 id tie-break keeps pages stable when two events share occurred_at.
CREATE INDEX item_event_item_occurred_idx ON item_event (item_id, occurred_at DESC, id DESC);

-- Backs ItemEventRepository.ListByBin's fixed-window recent-activity read
-- (NSTR-42's bin panel — LIMIT 10, no paging). Partial: bin_id is nullable
-- and a row with no bin is never part of this read. Because a 'removed'
-- event carries the bin the item LEFT (not just 'added' the bin it entered),
-- this index and query surface removals too, not only additions.
CREATE INDEX item_event_bin_occurred_idx ON item_event (bin_id, occurred_at DESC, id DESC) WHERE bin_id IS NOT NULL;

-- Append-only is enforced twice, not merely discouraged by convention:
--
-- 1) REVOKE the ordinary write privileges outright. PostgreSQL explicitly
--    allows an owner to revoke its own privileges (verified against the
--    PostgreSQL 17 GRANT docs), so this holds even though migrations run as
--    the same application role that owns the table and would otherwise
--    retain implicit owner privileges on it.
REVOKE UPDATE, DELETE, TRUNCATE ON item_event FROM PUBLIC, CURRENT_USER;

-- 2) A trigger backstop that holds even if a future migration re-grants
-- those privileges. FOR EACH STATEMENT because TRUNCATE has no per-row
-- semantics (verified against the PostgreSQL 17 CREATE TRIGGER docs: a
-- TRUNCATE trigger must be a statement-level trigger), and UPDATE/DELETE are
-- folded into the same statement-level trigger rather than a second
-- row-level one, since the function needs no access to OLD/NEW to raise.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION item_event_deny_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'item_event is append-only: % is not permitted', TG_OP;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER item_event_append_only
    BEFORE UPDATE OR DELETE OR TRUNCATE ON item_event
    FOR EACH STATEMENT
    EXECUTE FUNCTION item_event_deny_mutation();

-- +goose Down
DROP TRIGGER IF EXISTS item_event_append_only ON item_event;
DROP FUNCTION IF EXISTS item_event_deny_mutation();
DROP INDEX IF EXISTS item_event_bin_occurred_idx;
DROP INDEX IF EXISTS item_event_item_occurred_idx;
DROP TABLE IF EXISTS item_event;
