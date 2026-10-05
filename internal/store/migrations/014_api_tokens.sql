-- Tokens other apps (an event or production planner) use to read from
-- /api/. Only a hash is kept: the token itself is shown once, when it's
-- made, and its first few characters tell tokens apart afterwards.
CREATE TABLE api_tokens (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL,
    token_hash   TEXT    NOT NULL UNIQUE,
    prefix       TEXT    NOT NULL,
    created_by   INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    last_used_at TEXT
);
