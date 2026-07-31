-- +goose Up
-- pgcrypto is enabled for digest/crypt function availability and parity with
-- Nestova on the shared Postgres instance — NOT for UUID generation.
-- gen_random_uuid() has been built into core Postgres since version 13, so
-- this extension is not what makes UUID generation available.
-- Schema-qualified (not left to search_path resolution): NSTR-119 sets
-- search_path to nestorage,public, and CREATE SCHEMA IF NOT EXISTS nestorage
-- runs before this migration, so an unqualified CREATE EXTENSION would
-- otherwise install into nestorage instead of public.
CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public;

-- +goose Down
DROP EXTENSION IF EXISTS pgcrypto;
