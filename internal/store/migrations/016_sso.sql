-- foreign_keys: off
-- Single sign-on through User Management. The users table stays as a
-- local cache of the people the service knows, so every reference to
-- users(id) keeps working: sessions (ON DELETE CASCADE) and the
-- created_by / assigned_to / reported_by / requested_by columns on
-- maintenance_logs, supply_changes, problems, problem_updates, supplies
-- and api_tokens (all ON DELETE SET NULL).
--
-- password_hash becomes nullable (SSO users have none here), so users is
-- rebuilt. Foreign keys are off for this migration so dropping the old
-- table can't cascade into sessions or set those columns to NULL; ids are
-- copied as they are and the references are checked before it commits.
CREATE TABLE users_new (
    id            INTEGER PRIMARY KEY,
    username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    display_name  TEXT    NOT NULL DEFAULT '',
    password_hash TEXT,                  -- NULL: can't sign in locally
    is_admin      INTEGER NOT NULL DEFAULT 0,
    sso_subject   TEXT    UNIQUE,        -- the service's user ID; NULL until linked
    active        INTEGER NOT NULL DEFAULT 1,  -- 0: access removed, row kept for history
    created_at    TEXT    NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO users_new (id, username, display_name, password_hash, is_admin, created_at)
    SELECT id, username, display_name, password_hash, is_admin, created_at FROM users;
DROP TABLE users;
ALTER TABLE users_new RENAME TO users;

-- The grant behind an SSO session, and when it was last confirmed with
-- the service. Both NULL for local-login sessions.
ALTER TABLE sessions ADD COLUMN sso_grant TEXT;
ALTER TABLE sessions ADD COLUMN grant_checked_at TEXT;
