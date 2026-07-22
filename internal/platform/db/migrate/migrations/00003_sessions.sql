-- +goose Up
-- Server-side session store. The schema matches the exact layout required
-- by github.com/alexedwards/scs/pgxstore, which does not create its own
-- table — see internal/platform/session for the SessionManager built over
-- it.
CREATE TABLE sessions (
    token  text        PRIMARY KEY,
    data   bytea       NOT NULL,
    expiry timestamptz NOT NULL
);

CREATE INDEX sessions_expiry_idx ON sessions (expiry);

-- +goose Down
DROP INDEX IF EXISTS sessions_expiry_idx;
DROP TABLE IF EXISTS sessions;
