-- Run once by the separately held migrator in the dedicated database/schema.
-- No roles, credentials, database or schema are created by this migration.
CREATE TABLE portal.pending_logins (
    handle bytea PRIMARY KEY CHECK (octet_length(handle) = 32),
    generation text NOT NULL CHECK (length(generation) BETWEEN 32 AND 128),
    version smallint NOT NULL CHECK (version = 1),
    state_digest bytea NOT NULL CHECK (octet_length(state_digest) = 32),
    expires_at timestamptz NOT NULL,
    payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 28 AND 131072)
);
CREATE TABLE portal.sessions (
    handle bytea PRIMARY KEY CHECK (octet_length(handle) = 32),
    generation text NOT NULL CHECK (length(generation) BETWEEN 32 AND 128),
    version smallint NOT NULL CHECK (version = 1),
    expires_at timestamptz NOT NULL,
    payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 28 AND 131072)
);
