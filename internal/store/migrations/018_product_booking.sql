-- Products people can ask for when they book a room on the church
-- calendar. booking is '' (not offered), 'count' (ask how many) or
-- 'service' (ask yes or no, e.g. Sound). product_offers lists the places
-- a product is offered with; rows stay when a place stops being bookable.
ALTER TABLE products ADD COLUMN booking TEXT NOT NULL DEFAULT '' CHECK (booking IN ('','count','service'));

CREATE TABLE product_offers (
    product_id INTEGER NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    place_id   INTEGER NOT NULL REFERENCES places(id) ON DELETE CASCADE,
    PRIMARY KEY (product_id, place_id)
);
CREATE INDEX idx_product_offers_place ON product_offers(place_id);
