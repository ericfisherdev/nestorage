-- +goose Up
-- pgcrypto is enabled for digest/crypt function availability and parity with
-- Nestova on the shared Postgres instance — NOT for UUID generation.
-- gen_random_uuid() has been built into core Postgres since version 13, so
-- this extension is not what makes UUID generation available.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- +goose Down
DROP EXTENSION IF EXISTS pgcrypto;
