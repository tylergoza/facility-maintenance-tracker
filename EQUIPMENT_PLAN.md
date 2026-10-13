# Maintenance Tracker: equipment and services offered on the calendar

The cross-app design is in `../.claude/plans/bookable-equipment.md`. The
tracker owns products, so it is where a product is marked as something
people can ask for when they book a room (tables, chairs, the
inflatables), and where service products like **Sound** and **Lighting**
are made. The calendar reads this through the existing products and items
API. Nothing here changes how the tracker works alone; the setting is only
information until a calendar reads it.

The tracker is also the record of where equipment is. Setting up is
moving the items to the booked room (the existing **Move**); taking down
is moving them back. The calendar shows those places, so there are no
set-up or tear-down times to keep.

Status: planned (2026-10-13).

## What changes

- `products.booking TEXT NOT NULL DEFAULT ''`, one of:
  - `''` **Not offered** (default)
  - `count` **Ask how many** (up to the product's `total`; its items must be portable)
  - `service` **Ask yes or no, with a note** (Sound, Lighting; no count, no items needed)
- `product_offers(product_id, place_id)`: the places it's offered with.
  **None means not offered anywhere yet.** Only bookable places can be
  picked: rooms, areas, or a building when the building is bookable. A
  building offer is for booking the building whole; rooms inside need
  their own offer.
- The setting is on the **product**, not the item, so it is the same
  wherever the items are stored. `items` don't change.
- **The planners are not affected.** They read products and items without
  filtering on `booking`, and must keep doing that.

## Steps

### 1. The setting, offers and the API
- [ ] Migration `018_product_booking.sql`: `ALTER TABLE products ADD COLUMN booking TEXT NOT NULL DEFAULT '' CHECK (booking IN ('','count','service'))`; `CREATE TABLE product_offers (product_id INTEGER NOT NULL REFERENCES products(id) ON DELETE CASCADE, place_id INTEGER NOT NULL REFERENCES places(id) ON DELETE CASCADE, PRIMARY KEY (product_id, place_id))`
- [ ] `store.Product.Booking` and `Offers []int`; read in the product loads, written when the product is saved. Clearing `booking` keeps the offers (so it can be turned back on), but the API doesn't send them
- [ ] Product form (`web/templates/pages/products/form.html`, between the tracking choice and notes): an **On the church calendar** radio (Not offered / Ask how many / Ask yes or no) and an **Offered with** list of bookable places, grouped by building, with a bookable building pickable on its own ("the whole building"; it doesn't tick the rooms inside). With the setting on and nothing ticked, the page says "Not offered anywhere yet". Saved through `POST /products/{id}`. The item form's product fields only apply when a new product is made, so this lives on the product form only, with a link to it from the item page ("Offer on the calendar")
- [ ] **Ask how many** needs portable items: saving it when some items aren't portable shows a warning listing them, with a link to fix (they can't be moved for a set-up)
- [ ] Service products with no items: check a product can be made and kept with no items (a **New product** on `/products` if there isn't one), and that its page doesn't look broken with nothing under it ("A service: no items")
- [ ] `counted` and units don't matter here: `total` is the sum of the items' quantities either way
- [ ] Products list: a "Calendar" tag on offered products and a `?booking=1` filter, next to the category filter
- [ ] API: `booking` and `offered_in` (place IDs) in the product JSON (`internal/server/api.go`); `GET /api/v1/products?booking=1` lists only products with the setting on **and** at least one offer (combines with the other filters). `total` is already there. Where the items are comes from the existing `GET /api/v1/items?product=` (`place{id,path}`, `quantity`); no new endpoint
- [ ] A place that stops being bookable, or is deleted, drops out of `offered_in` (deleted: cascade; unbookable: filtered in the API, kept in the table so it comes back). The place's delete confirm and the "not bookable" save list the products offered there
- [ ] Tests: migration, save/load, the CHECK, offers only bookable places (a bookable building is allowed, and doesn't add its rooms), no offers = not in `?booking=1`, the portable warning, API fields, a service product with no items, `total` for counted and unit products

### 2. Seed and docs
- [ ] `seed.go`: Folding tables and Chairs (`count`, offered with the sample bookable building and two bookable rooms), Inflatables (`count`, Life Center), Sound and Lighting (`service`, no items, offered with the sample building)
- [ ] README: the products API fields, "set-ups are moves", and a line in the "running alone / connected" table: "Calendar: products set **On the church calendar** can be asked for with a room booking in the places they're offered with"

### 3. Set-ups coming up (later, needs the calendar's step 6)
Off unless the **Calendar** connection is set, like **Coming up here**.
- [ ] `internal/calendar`: decode `equipment` (name, `tracker_product_id`, quantity, note, short) on events from the calendar's read API
- [ ] `/setups`: the next two weeks of bookings that asked for equipment, by day: room, times, what and how many, the note, "short by N". For each counted line, where the items are now, and a **Set up** button that opens the move page for that product's items with the booked room picked. Linked from the menu when the calendar connection is on
- [ ] **Still set up**: offered items whose last move took them into a place they're offered with (so they were set up there and not moved back yet), with a **Take down** button that opens the move page with the place they came from (the last move in the item's history) picked
- [ ] A room's **Coming up here** shows the equipment under each booking
- [ ] A product's page shows "Asked for: 30 on Sat 18 Oct (Life Center)" for the next two weeks
- [ ] Tests: connection off hides the menu and pages; calendar down shows the one-line note; Set up / Take down prefill the move page; equipment matched to products by tracker ID, then name
