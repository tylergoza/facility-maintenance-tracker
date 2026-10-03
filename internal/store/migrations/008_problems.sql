-- Problems people have reported (a leak, a door that sticks, a light out)
-- and who, if anyone, is working on them.
CREATE TABLE problems (
    id               INTEGER PRIMARY KEY,
    building_id      INTEGER NOT NULL REFERENCES buildings(id) ON DELETE CASCADE,
    room_id          INTEGER REFERENCES rooms(id) ON DELETE SET NULL,
    item_id          INTEGER REFERENCES items(id) ON DELETE SET NULL,
    title            TEXT    NOT NULL,
    details          TEXT    NOT NULL DEFAULT '',
    status           TEXT    NOT NULL DEFAULT 'open' CHECK (status IN ('open','in_progress','resolved')),
    assigned_to      INTEGER REFERENCES users(id) ON DELETE SET NULL,
    -- Who reported it. reported_by is set when they were signed in;
    -- reporter_name is always kept so the record survives the user.
    reported_by      INTEGER REFERENCES users(id) ON DELETE SET NULL,
    reporter_name    TEXT    NOT NULL DEFAULT '',
    reporter_contact TEXT    NOT NULL DEFAULT '',
    created_at       TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at       TEXT    NOT NULL DEFAULT (datetime('now')),
    resolved_at      TEXT
);
CREATE INDEX idx_problems_status ON problems(status, building_id);
CREATE INDEX idx_problems_room ON problems(room_id);
CREATE INDEX idx_problems_item ON problems(item_id);

-- Timeline of a problem: status changes, assignments and notes, newest
-- last. NULL status/assigned_name mean "unchanged"; an empty
-- assigned_name means it was unassigned.
CREATE TABLE problem_updates (
    id            INTEGER PRIMARY KEY,
    problem_id    INTEGER NOT NULL REFERENCES problems(id) ON DELETE CASCADE,
    status        TEXT CHECK (status IN ('open','in_progress','resolved')),
    assigned_name TEXT,
    note          TEXT    NOT NULL DEFAULT '',
    created_by    INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at    TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_problem_updates_problem ON problem_updates(problem_id, id);

-- Off until an admin turns it on: anyone may report a problem without
-- signing in.
INSERT INTO settings (key, value) VALUES ('public_reports', '0');
