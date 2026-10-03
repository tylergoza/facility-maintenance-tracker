-- Reusable supplies (mop heads, rags, towels) are washed rather than used
-- up. Their count is split three ways: clean on hand (quantity), in use,
-- and out for cleaning.
ALTER TABLE supplies ADD COLUMN reusable INTEGER NOT NULL DEFAULT 0;
ALTER TABLE supplies ADD COLUMN in_use   INTEGER NOT NULL DEFAULT 0 CHECK (in_use >= 0);
ALTER TABLE supplies ADD COLUMN cleaning INTEGER NOT NULL DEFAULT 0 CHECK (cleaning >= 0);

-- Rebuild supply_changes to widen the kind CHECK (SQLite can't alter it)
-- and record how many moved plus every bucket's count afterwards.
CREATE TABLE supply_changes_new (
    id             INTEGER PRIMARY KEY,
    supply_id      INTEGER NOT NULL REFERENCES supplies(id) ON DELETE CASCADE,
    kind           TEXT    NOT NULL CHECK (kind IN ('added','used','restocked','counted',
                                                    'put_in_use','sent_cleaning','swapped','returned','retired')),
    amount         INTEGER NOT NULL DEFAULT 0,
    delta          INTEGER NOT NULL, -- change to clean on-hand quantity
    quantity_after INTEGER NOT NULL,
    in_use_after   INTEGER NOT NULL DEFAULT 0,
    cleaning_after INTEGER NOT NULL DEFAULT 0,
    note           TEXT    NOT NULL DEFAULT '',
    created_by     INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at     TEXT    NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO supply_changes_new (id, supply_id, kind, amount, delta, quantity_after, note, created_by, created_at)
    SELECT id, supply_id, kind, abs(delta), delta, quantity_after, note, created_by, created_at FROM supply_changes;
DROP TABLE supply_changes;
ALTER TABLE supply_changes_new RENAME TO supply_changes;
CREATE INDEX idx_supply_changes_supply ON supply_changes(supply_id, id);
