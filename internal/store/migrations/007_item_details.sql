-- An item can be a group of identical things counted together (10 outlets,
-- 15 lights, 2 air returns) and can use a supply that gets replaced (1 bulb
-- or filter each). Portable items (projectors, TVs) move between rooms.
ALTER TABLE items ADD COLUMN quantity   INTEGER NOT NULL DEFAULT 1 CHECK (quantity >= 1);
ALTER TABLE items ADD COLUMN supply_id  INTEGER REFERENCES supplies(id) ON DELETE SET NULL;
ALTER TABLE items ADD COLUMN supply_per INTEGER NOT NULL DEFAULT 1 CHECK (supply_per >= 1);
ALTER TABLE items ADD COLUMN portable   INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_items_supply ON items(supply_id);

-- History entries are maintenance work (a task or other work), a
-- replacement of the item's supply, or a move to another room.
-- replaced is how many of the item's supply were replaced, whether or not
-- they came out of stock; NULL when none were.
ALTER TABLE maintenance_logs ADD COLUMN kind TEXT NOT NULL DEFAULT 'work' CHECK (kind IN ('work','replaced','moved'));
ALTER TABLE maintenance_logs ADD COLUMN replaced INTEGER CHECK (replaced >= 1);
