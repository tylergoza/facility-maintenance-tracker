-- foreign_keys: off
-- What an item is ("Folding chairs", "Lapel mics", "Lights") becomes a
-- product, so the same thing in many places adds up: 300 chairs across
-- four rooms are four items of one product. Items keep where they are,
-- how many, their make and model, and the supply they use.
--
-- Counted products (chairs, tables) are only ever counted: their items
-- have no numbered units, IDs or notes. Everything starts out tracked,
-- one by one, as before.
CREATE TABLE products (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    category   TEXT    NOT NULL DEFAULT '',
    counted    INTEGER NOT NULL DEFAULT 0,
    notes      TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- One product per name, however it was capitalised; the first item's
-- spelling wins (SQLite takes bare columns from the MIN(id) row).
INSERT INTO products (name, created_at)
    SELECT name, created_at FROM (SELECT name, created_at, MIN(id) FROM items GROUP BY name COLLATE NOCASE);
-- Its category is the one its items use most.
UPDATE products SET category = COALESCE((
    SELECT i.category FROM items i
    WHERE i.name = products.name COLLATE NOCASE AND i.category != ''
    GROUP BY i.category COLLATE NOCASE ORDER BY COUNT(*) DESC, MIN(i.id) LIMIT 1), '');

CREATE TABLE items_new (
    id            INTEGER PRIMARY KEY,
    product_id    INTEGER NOT NULL REFERENCES products(id),
    place_id      INTEGER NOT NULL REFERENCES places(id),
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
INSERT INTO items_new (id, product_id, place_id, manufacturer, model, serial_number, install_date, notes,
                       quantity, supply_id, supply_per, portable, created_at)
    SELECT i.id, p.id, i.place_id, i.manufacturer, i.model, i.serial_number, i.install_date, i.notes,
           i.quantity, i.supply_id, i.supply_per, i.portable, i.created_at
    FROM items i JOIN products p ON p.name = i.name COLLATE NOCASE;
DROP TABLE items;
ALTER TABLE items_new RENAME TO items;
CREATE INDEX idx_items_product ON items(product_id);
CREATE INDEX idx_items_place ON items(place_id);
CREATE INDEX idx_items_supply ON items(supply_id);
