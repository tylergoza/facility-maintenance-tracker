# Maintenance Tracker: the Church Calendar

The design is in `../calendar/PLAN.md`. The tracker owns the rooms; the
calendar only reads them through the existing read-only API, so **nothing
here is required** for the calendar to work.

Status: built (2026-10-12), except making the token, which is done in the running app.

## Required

- [ ] Make a tracker API token named "Church calendar" for the calendar's room import (paste it into the calendar's **Connections → Tracker**, or `TRACKER_TOKEN` from the Keychain helper `with-cal`)
- [x] Check `GET /api/v1/places` gives the calendar what it needs (ID, name, full path, archived). If it doesn't, add the missing field there, not a new endpoint
  - It has `id`, `name`, `number`, `path` (full path) and `kind`. There's no `archived`: places
    here are deleted, not archived, so there's nothing to report. **The calendar's import should
    archive a room whose `tracker_place_id` is missing from the list** (and `GET /places/{id}`
    answers 404 for it). A room's display name is `name`, or "`number` – `name`" when `number`
    is set (the tracker's label); the tracker matches bookings on either.

## Optional, later

Useful once the calendar is in use, and off unless the **Calendar** connection (address and a calendar
**read** token) is set on `/admin/connections` (see `CONNECTIONS_PLAN.md`):

- [x] A room's page shows **Coming up here**: the next two weeks of bookings in that room from the calendar's read API (`?room=`), busy blocks as "Booked"
- [x] A work order on a room can warn when the room is booked on the day it's planned
  - The tracker's "work order" is a maintenance task; its due date is the planned day (today
    when overdue). The warning is on the room's page, in **Coming up here**.

Built in `internal/calendar` (read client), `internal/server/calendar_upcoming.go` and
`web/templates/pages/places/show.html`. It calls the calendar's read API as designed in
`../calendar/PLAN.md`: `GET /api/v1/events?from=&to=&room=<place name>` with a read token,
expecting `{"events": [...]}` with `busy`, `title`, `status`, `starts_on`, `start_time`,
`ends_on`, `end_time`, `rooms` (names) and `url`. Answers are filtered again by room name, so
it's safe if the calendar matches `room=` differently. Cached a minute per place, 3-second
timeout; a calendar that's down shows a one-line note.

## Bookable rooms

- [x] Places can be marked **Can be booked**; the calendar's room request form lists them
  from `GET /api/v1/places?bookable=1`. See `BOOKING_PLAN.md`.
