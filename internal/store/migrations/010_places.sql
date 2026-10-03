-- foreign_keys: off
-- Buildings and rooms become one tree of places:
--   Site › Building › Floor › Zone › Room › Area
-- Levels can be skipped (a porch is a zone straight under its building),
-- but a place always sits under a higher level, and only sites have no
-- parent. Items, supplies and problems each belong to one place.
CREATE TABLE places (
    id          INTEGER PRIMARY KEY,
    parent_id   INTEGER REFERENCES places(id),
    kind        TEXT    NOT NULL CHECK (kind IN ('site','building','floor','zone','room','area')),
    number      TEXT    NOT NULL DEFAULT '', -- e.g. room "104"; sorted naturally
    name        TEXT    NOT NULL,
    address     TEXT    NOT NULL DEFAULT '',
    notes       TEXT    NOT NULL DEFAULT '',
    -- The building or room this was before places, so old links and
    -- printed QR codes (/report?room=3) still work.
    building_id INTEGER UNIQUE,
    room_id     INTEGER UNIQUE,
    created_at  TEXT    NOT NULL DEFAULT (datetime('now')),
    CHECK ((kind = 'site') = (parent_id IS NULL))
);
CREATE INDEX idx_places_parent ON places(parent_id);

-- Everything that exists now goes on one site, named after the app.
INSERT INTO places (kind, name)
    SELECT 'site', COALESCE((SELECT value FROM settings WHERE key = 'site_name'), 'Main site')
    WHERE EXISTS (SELECT 1 FROM buildings);

INSERT INTO places (parent_id, kind, name, address, notes, building_id, created_at)
    SELECT (SELECT id FROM places WHERE kind = 'site'), 'building', name, address, notes, id, created_at
    FROM buildings ORDER BY id;

-- A room's "Floor / area" text becomes a floor inside its building.
INSERT INTO places (parent_id, kind, name)
    SELECT DISTINCT b.id, 'floor', r.floor
    FROM rooms r JOIN places b ON b.building_id = r.building_id
    WHERE r.floor != '';

INSERT INTO places (parent_id, kind, number, name, notes, room_id, created_at)
    SELECT COALESCE(f.id, b.id), 'room', r.number, r.name, r.notes, r.id, r.created_at
    FROM rooms r
    JOIN places b ON b.building_id = r.building_id
    LEFT JOIN places f ON f.parent_id = b.id AND f.kind = 'floor' AND f.name = r.floor
    ORDER BY r.id;

-- Rebuild supplies, items and problems around place_id.
CREATE TABLE supplies_new (
    id          INTEGER PRIMARY KEY,
    place_id    INTEGER NOT NULL REFERENCES places(id),
    name        TEXT    NOT NULL,
    unit        TEXT    NOT NULL DEFAULT '',
    quantity    INTEGER NOT NULL DEFAULT 0 CHECK (quantity >= 0),
    reorder_at  INTEGER NOT NULL DEFAULT 0 CHECK (reorder_at >= 0),
    notes       TEXT    NOT NULL DEFAULT '',
    reusable    INTEGER NOT NULL DEFAULT 0,
    in_use      INTEGER NOT NULL DEFAULT 0 CHECK (in_use >= 0),
    cleaning    INTEGER NOT NULL DEFAULT 0 CHECK (cleaning >= 0),
    created_at  TEXT    NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO supplies_new (id, place_id, name, unit, quantity, reorder_at, notes, reusable, in_use, cleaning, created_at)
    SELECT s.id, COALESCE(r.id, b.id), s.name, s.unit, s.quantity, s.reorder_at, s.notes, s.reusable, s.in_use, s.cleaning, s.created_at
    FROM supplies s
    JOIN places b ON b.building_id = s.building_id
    LEFT JOIN places r ON r.room_id = s.room_id;
DROP TABLE supplies;
ALTER TABLE supplies_new RENAME TO supplies;
CREATE INDEX idx_supplies_place ON supplies(place_id);

CREATE TABLE items_new (
    id            INTEGER PRIMARY KEY,
    place_id      INTEGER NOT NULL REFERENCES places(id),
    name          TEXT    NOT NULL,
    category      TEXT    NOT NULL DEFAULT '',
    manufacturer  TEXT    NOT NULL DEFAULT '',
    model         TEXT    NOT NULL DEFAULT '',
    serial_number TEXT    NOT NULL DEFAULT '',
    install_date  TEXT,
    notes         TEXT    NOT NULL DEFAULT '',
    quantity      INTEGER NOT NULL DEFAULT 1 CHECK (quantity >= 1),
    supply_id     INTEGER REFERENCES supplies(id) ON DELETE SET NULL,
    supply_per    INTEGER NOT NULL DEFAULT 1 CHECK (supply_per >= 1),
    portable      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT    NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO items_new (id, place_id, name, category, manufacturer, model, serial_number, install_date, notes,
                       quantity, supply_id, supply_per, portable, created_at)
    SELECT i.id, COALESCE(r.id, b.id), i.name, i.category, i.manufacturer, i.model, i.serial_number, i.install_date, i.notes,
           i.quantity, i.supply_id, i.supply_per, i.portable, i.created_at
    FROM items i
    JOIN places b ON b.building_id = i.building_id
    LEFT JOIN places r ON r.room_id = i.room_id;
DROP TABLE items;
ALTER TABLE items_new RENAME TO items;
CREATE INDEX idx_items_place ON items(place_id);
CREATE INDEX idx_items_supply ON items(supply_id);

CREATE TABLE problems_new (
    id               INTEGER PRIMARY KEY,
    place_id         INTEGER NOT NULL REFERENCES places(id),
    item_id          INTEGER REFERENCES items(id) ON DELETE SET NULL,
    unit             INTEGER CHECK (unit >= 1),
    title            TEXT    NOT NULL,
    details          TEXT    NOT NULL DEFAULT '',
    status           TEXT    NOT NULL DEFAULT 'open' CHECK (status IN ('open','in_progress','resolved')),
    assigned_to      INTEGER REFERENCES users(id) ON DELETE SET NULL,
    reported_by      INTEGER REFERENCES users(id) ON DELETE SET NULL,
    reporter_name    TEXT    NOT NULL DEFAULT '',
    reporter_contact TEXT    NOT NULL DEFAULT '',
    created_at       TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at       TEXT    NOT NULL DEFAULT (datetime('now')),
    resolved_at      TEXT
);
INSERT INTO problems_new (id, place_id, item_id, unit, title, details, status, assigned_to, reported_by,
                          reporter_name, reporter_contact, created_at, updated_at, resolved_at)
    SELECT p.id, COALESCE(r.id, b.id), p.item_id, p.unit, p.title, p.details, p.status, p.assigned_to, p.reported_by,
           p.reporter_name, p.reporter_contact, p.created_at, p.updated_at, p.resolved_at
    FROM problems p
    JOIN places b ON b.building_id = p.building_id
    LEFT JOIN places r ON r.room_id = p.room_id;
DROP TABLE problems;
ALTER TABLE problems_new RENAME TO problems;
CREATE INDEX idx_problems_status ON problems(status);
CREATE INDEX idx_problems_place ON problems(place_id);
CREATE INDEX idx_problems_item ON problems(item_id);

DROP TABLE rooms;
DROP TABLE buildings;

-- Optional labels that cut across the tree, e.g. "classrooms" or "media".
CREATE TABLE tags (
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE
);
CREATE TABLE place_tags (
    place_id INTEGER NOT NULL REFERENCES places(id) ON DELETE CASCADE,
    tag_id   INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (place_id, tag_id)
);
CREATE INDEX idx_place_tags_tag ON place_tags(tag_id);
