-- The things an item counts are numbered #1..#quantity (a sticker on each
-- light, say). A unit only has a row here once it's given a location note.
CREATE TABLE item_units (
    item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    number  INTEGER NOT NULL CHECK (number >= 1),
    label   TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (item_id, number)
);

-- Which units a history entry was about, e.g. bulbs replaced in #3 and #7.
CREATE TABLE log_units (
    log_id INTEGER NOT NULL REFERENCES maintenance_logs(id) ON DELETE CASCADE,
    unit   INTEGER NOT NULL CHECK (unit >= 1),
    PRIMARY KEY (log_id, unit)
);

-- Which one of the item a problem is about, once staff know.
ALTER TABLE problems ADD COLUMN unit INTEGER CHECK (unit >= 1);
