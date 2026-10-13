# Maintenance Tracker: marking places bookable

The cross-app design is in
`../.claude/plans/bookable-rooms-and-pending-bookings.md`. The tracker owns
places, so it is where a building, room or area is marked **Can be
booked**. The calendar reads the flag through the existing places API.
Nothing here changes how the tracker works alone; the flag is only
information until a calendar reads it.

Status: built (2026-10-13).

## What changes

- `places.bookable INTEGER NOT NULL DEFAULT 0`. Off by default: someone
  chooses which spaces the public can ask for. Only meaningful for kinds
  `building`, `room` and `area`. Sites and floors always save it as 0.
- **The planners are not affected.** They list rooms from this API
  without filtering on `bookable`, and they must keep doing that: the
  event and production planners can always use every room.

## Steps

### 1. The flag
- [x] Migration `017_place_bookable.sql`: `ALTER TABLE places ADD COLUMN bookable INTEGER NOT NULL DEFAULT 0` (plain add; no table rebuild needed, since no CHECK changes)
- [x] `store.Place.Bookable`; read in `loadPlaces` (`internal/store/places.go` SELECT), written in `savePlaceTx`; forced to false for `site`/`floor`
- [x] Place form (`web/templates/pages/places/form.html`): a **Can be booked on the church calendar** checkbox, shown for building/room/area only (the kind radio's Stimulus controller already shows/hides fields per kind; reuse it). Read in `placeFromForm` (`internal/server/handlers_places.go`)
- [x] Place page: a small "Bookable" tag next to the kind; the places list can filter on it (`?bookable=1`), next to the tag filter
- [x] Bulk edit (`/places/bulk`): **Mark bookable** / **Mark not bookable** for the ticked places, so a whole building's rooms can be done at once
  - `/places/bulk` only adds places, so it got a **Can be booked** box for the places it adds;
    ticking existing places is a new page, `/places/bookable` (`?parent=` for one building,
    linked from a building's page as **Choose bookable rooms**)
- [x] API: `bookable` in `apiPlaceJSON` / `placeJSON` (`internal/server/api.go`); `GET /api/v1/places?bookable=1` lists only bookable places (combines with `?tag=`)
- [x] README: the places API fields, and a line in the "running alone / connected" table: "Calendar: places marked **Can be booked** appear on its room request form"
- [x] Tests: migration, save/load, site and floor never bookable, API field and `?bookable=1` filter

### 2. Seed and docs
- [x] `seed.go`: mark a few sample rooms and one building bookable
- [x] Tick `CALENDAR_PLAN.md` with a pointer here
