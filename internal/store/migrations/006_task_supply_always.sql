-- Whether a task's linked supply is used up every time it's done ("Replace
-- filter") or only sometimes ("Check filter": replaced only when dirty).
-- Existing links keep the every-time behaviour.
ALTER TABLE tasks ADD COLUMN supply_always INTEGER NOT NULL DEFAULT 1;
