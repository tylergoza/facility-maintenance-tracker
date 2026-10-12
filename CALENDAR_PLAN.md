# Maintenance Tracker: the Church Calendar

The design is in `../calendar/PLAN.md`. The tracker owns the rooms; the
calendar only reads them through the existing read-only API, so **nothing
here is required** for the calendar to work.

Status: planned, nothing built yet (2026-10-11).

## Required

- [ ] Make a tracker API token named "Church calendar" for the calendar's room import (paste it into the calendar's **Connections → Tracker**, or `TRACKER_TOKEN` from the Keychain helper `with-cal`)
- [ ] Check `GET /api/v1/places` gives the calendar what it needs (ID, name, full path, archived). If it doesn't, add the missing field there, not a new endpoint

## Optional, later

Useful once the calendar is in use, and off unless the **Calendar** connection (address and a calendar
**read** token) is set on `/admin/connections` (see `CONNECTIONS_PLAN.md`):

- [ ] A room's page shows **Coming up here**: the next two weeks of bookings in that room from the calendar's read API (`?room=`), busy blocks as "Booked"
- [ ] A work order on a room can warn when the room is booked on the day it's planned
