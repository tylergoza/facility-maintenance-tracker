-- Optional room number (e.g. "104", "B12"). Text so wings/letters work;
-- the app sorts it naturally so "9" comes before "10".
ALTER TABLE rooms ADD COLUMN number TEXT NOT NULL DEFAULT '';
