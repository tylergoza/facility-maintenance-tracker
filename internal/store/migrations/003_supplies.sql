-- Consumable supplies (paper towels, light bulbs, filters…) kept in a
-- building and optionally a specific room. Quantity is a whole-unit count.
CREATE TABLE supplies (
    id          INTEGER PRIMARY KEY,
    building_id INTEGER NOT NULL REFERENCES buildings(id) ON DELETE CASCADE,
    room_id     INTEGER REFERENCES rooms(id) ON DELETE SET NULL,
    name        TEXT    NOT NULL,
    unit        TEXT    NOT NULL DEFAULT '',
    quantity    INTEGER NOT NULL DEFAULT 0 CHECK (quantity >= 0),
    -- Flag as low when quantity is at or below this; 0 = only flag when out.
    reorder_at  INTEGER NOT NULL DEFAULT 0 CHECK (reorder_at >= 0),
    notes       TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_supplies_building ON supplies(building_id);
CREATE INDEX idx_supplies_room ON supplies(room_id);

-- Every change to a supply's quantity, newest last.
CREATE TABLE supply_changes (
    id             INTEGER PRIMARY KEY,
    supply_id      INTEGER NOT NULL REFERENCES supplies(id) ON DELETE CASCADE,
    kind           TEXT    NOT NULL CHECK (kind IN ('added','used','restocked','counted')),
    delta          INTEGER NOT NULL,
    quantity_after INTEGER NOT NULL,
    note           TEXT    NOT NULL DEFAULT '',
    created_by     INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at     TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_supply_changes_supply ON supply_changes(supply_id, id);
