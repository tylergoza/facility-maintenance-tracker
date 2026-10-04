-- Every unit gets an ID, what's on its sticker ("2", "Mic 2"), starting
-- out as its number. The ID stays with the thing when moves renumber the
-- units around it. Units past an item's count are gone, so their rows go.
ALTER TABLE item_units ADD COLUMN tag TEXT NOT NULL DEFAULT '';
DELETE FROM item_units WHERE number > (SELECT quantity FROM items WHERE id = item_units.item_id);
WITH RECURSIVE n(item_id, number, quantity) AS (
    SELECT id, 1, quantity FROM items
    UNION ALL
    SELECT item_id, number + 1, quantity FROM n WHERE number < quantity
)
INSERT OR IGNORE INTO item_units (item_id, number) SELECT item_id, number FROM n;
UPDATE item_units SET tag = number WHERE tag = '';
