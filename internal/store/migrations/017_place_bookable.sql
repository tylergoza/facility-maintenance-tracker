-- Buildings, rooms and areas the public can ask to book on the church
-- calendar. Off by default; sites and floors are never bookable. The
-- calendar reads it through GET /api/v1/places?bookable=1.
ALTER TABLE places ADD COLUMN bookable INTEGER NOT NULL DEFAULT 0;
