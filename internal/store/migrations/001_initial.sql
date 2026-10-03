-- Initial schema for the maintenance tracker.
-- Dates are stored as ISO-8601 text (YYYY-MM-DD) so the database stays
-- readable with the plain sqlite3 CLI and portable between hosts.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    display_name  TEXT    NOT NULL DEFAULT '',
    password_hash TEXT    NOT NULL,
    is_admin      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE sessions (
    token      TEXT    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token TEXT    NOT NULL,
    expires_at TEXT    NOT NULL,
    created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_sessions_user ON sessions(user_id);

CREATE TABLE buildings (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    address    TEXT NOT NULL DEFAULT '',
    notes      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE rooms (
    id          INTEGER PRIMARY KEY,
    building_id INTEGER NOT NULL REFERENCES buildings(id) ON DELETE CASCADE,
    name        TEXT    NOT NULL,
    floor       TEXT    NOT NULL DEFAULT '',
    notes       TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_rooms_building ON rooms(building_id);

-- Items always belong to a building; room is optional so building-wide
-- things (roof, parking lot, boiler, fire alarm panel) can be tracked too.
CREATE TABLE items (
    id            INTEGER PRIMARY KEY,
    building_id   INTEGER NOT NULL REFERENCES buildings(id) ON DELETE CASCADE,
    room_id       INTEGER REFERENCES rooms(id) ON DELETE SET NULL,
    name          TEXT    NOT NULL,
    category      TEXT    NOT NULL DEFAULT '',
    manufacturer  TEXT    NOT NULL DEFAULT '',
    model         TEXT    NOT NULL DEFAULT '',
    serial_number TEXT    NOT NULL DEFAULT '',
    install_date  TEXT,
    notes         TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_items_building ON items(building_id);
CREATE INDEX idx_items_room ON items(room_id);

-- A task is a (possibly recurring) piece of maintenance for an item.
-- interval_value/interval_unit are NULL for one-off tasks.
CREATE TABLE tasks (
    id                INTEGER PRIMARY KEY,
    item_id           INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    name              TEXT    NOT NULL,
    description       TEXT    NOT NULL DEFAULT '',
    interval_value    INTEGER,
    interval_unit     TEXT CHECK (interval_unit IN ('days','weeks','months','years')),
    last_completed_on TEXT,
    next_due_on       TEXT,
    active            INTEGER NOT NULL DEFAULT 1,
    created_at        TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_tasks_item ON tasks(item_id);
CREATE INDEX idx_tasks_due ON tasks(active, next_due_on);

-- History of maintenance actually performed.
CREATE TABLE maintenance_logs (
    id           INTEGER PRIMARY KEY,
    item_id      INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    task_id      INTEGER REFERENCES tasks(id) ON DELETE SET NULL,
    performed_on TEXT    NOT NULL,
    performed_by TEXT    NOT NULL DEFAULT '',
    cost_cents   INTEGER,
    notes        TEXT    NOT NULL DEFAULT '',
    created_by   INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at   TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_logs_item ON maintenance_logs(item_id, performed_on);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
INSERT INTO settings (key, value) VALUES
    ('site_name', 'Facility Maintenance'),
    ('due_soon_days', '30');
