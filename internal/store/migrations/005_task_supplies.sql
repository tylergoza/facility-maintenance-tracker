-- A task can use a supply each time it's done, e.g. "Replace filter" uses
-- one 20x25x1 filter.
ALTER TABLE tasks ADD COLUMN supply_id INTEGER REFERENCES supplies(id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN supply_amount INTEGER NOT NULL DEFAULT 1 CHECK (supply_amount >= 1);

-- Supply taken when recording maintenance points at that history entry, so
-- deleting the entry can put the supply back.
ALTER TABLE supply_changes ADD COLUMN log_id INTEGER REFERENCES maintenance_logs(id) ON DELETE SET NULL;
CREATE INDEX idx_supply_changes_log ON supply_changes(log_id);
