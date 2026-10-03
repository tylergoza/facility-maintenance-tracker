-- foreign_keys: off
-- Zones fold into areas. An area can now hold floors, rooms and other
-- areas, and go straight on a site or building (outside spaces).
-- Rebuilt because SQLite can't change a CHECK constraint in place.
CREATE TABLE places_new (
    id          INTEGER PRIMARY KEY,
    parent_id   INTEGER REFERENCES places(id),
    kind        TEXT    NOT NULL CHECK (kind IN ('site','building','floor','room','area')),
    number      TEXT    NOT NULL DEFAULT '',
    name        TEXT    NOT NULL,
    address     TEXT    NOT NULL DEFAULT '',
    notes       TEXT    NOT NULL DEFAULT '',
    building_id INTEGER UNIQUE,
    room_id     INTEGER UNIQUE,
    created_at  TEXT    NOT NULL DEFAULT (datetime('now')),
    CHECK ((kind = 'site') = (parent_id IS NULL))
);
INSERT INTO places_new (id, parent_id, kind, number, name, address, notes, building_id, room_id, created_at)
    SELECT id, parent_id, CASE kind WHEN 'zone' THEN 'area' ELSE kind END, number, name, address, notes, building_id, room_id, created_at
    FROM places;
DROP TABLE places;
ALTER TABLE places_new RENAME TO places;
CREATE INDEX idx_places_parent ON places(parent_id);

-- A shared area (a stairwell between two floors, a balcony off two rooms)
-- lives in one place, its parent_id, and is also in these. It shows up in
-- each of them and counts toward what's in them.
CREATE TABLE place_links (
    place_id  INTEGER NOT NULL REFERENCES places(id) ON DELETE CASCADE,
    parent_id INTEGER NOT NULL REFERENCES places(id) ON DELETE CASCADE,
    PRIMARY KEY (place_id, parent_id),
    CHECK (place_id != parent_id)
);
CREATE INDEX idx_place_links_parent ON place_links(parent_id);
