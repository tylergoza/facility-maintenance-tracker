-- Public problem reports are split: "public_reports" now covers places
-- (QR codes on doors) and "public_item_reports" covers items (stickers on
-- the things themselves). Items could be reported along with places
-- before, so they start out the same.
INSERT OR IGNORE INTO settings (key, value)
    SELECT 'public_item_reports', value FROM settings WHERE key = 'public_reports';

-- Someone asking for more of a supply (from its page, often after
-- scanning its QR code). Restocking it clears the request.
ALTER TABLE supplies ADD COLUMN requested_at TEXT;
ALTER TABLE supplies ADD COLUMN requested_by INTEGER REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE supplies ADD COLUMN request_note TEXT NOT NULL DEFAULT '';
