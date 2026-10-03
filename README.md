# Facility Maintenance Tracker

Track buildings, rooms, and the equipment in them, with scheduled and recurring
maintenance. A public dashboard shows what's overdue and coming up; signed-in
users make changes. Installable as an app (PWA) on phones and desktops.

- **Backend:** Go, single static binary with templates/JS/CSS embedded
- **Database:** SQLite (one file, pure-Go driver with no CGO)
- **Frontend:** server-rendered HTML + [Stimulus](https://stimulus.hotwired.dev) 3.2 with a custom
  autoloader. No Node, no bundler, no Turbo/Hotwire.

## Quick start

```sh
make dev                     # http://127.0.0.1:8080, edits to web/ show on refresh
```

The first visit takes you to `/setup` to create the admin account. To try it
with sample data:

```sh
go run . seed-demo
```

## Features

| Area | What it does |
|------|--------------|
| Dashboard (`/`, public) | Status tiles (Overdue / Due soon / Upcoming / Not scheduled), per-building health, search and status filters (kept in the URL hash), building filter. Works offline once visited. |
| Buildings → Rooms → Items | Items belong to a building and optionally a room (roofs, parking lots and HVAC units can be building-wide). An item can count a group of identical things (10 outlets, 15 lights) and use a supply that gets replaced (1 bulb or filter each); "Replace" records it and takes it from stock. Portable items (projectors, TV carts) have "Move", and every change of location is kept in their history. Room pages group items by category. Deleting a room keeps its items. |
| Tasks | One-time or recurring (every N days/weeks/months/years). "Next due" is calculated from "last done" or set by hand. Month math clamps (Jan 31 + 1 month = Feb 28). |
| Mark done | Logs date, who, cost and notes, then rolls the task forward. Back-dated entries never move the schedule backwards. One-time tasks close. |
| History | Per-item and site-wide maintenance logs, including ad-hoc "other work". |
| Supplies | Counts of things that get used up (paper towels, filters, bulbs) or washed and reused (mop heads), with a history of every change and low-stock flags. Tasks can take a supply each time they're done. |
| Problems | Report a problem against a building, room or item; mark "I'm on it", assign, add notes, resolve. Only signed-in users can see reports. Admins can let anyone report at `/report` without signing in (off by default, 10 reports an hour per device, with a honeypot field for bots). |
| Users | Admins manage users, settings (site name, "due soon" window, public problem reports) and can download a backup. Editors manage data. |

## Stimulus autoloader

`web/static/js/stimulus_autoloader.js` replaces the deprecated
`stimulus-loading`/`lazyLoadControllersFrom` behavior. It watches the DOM with a
`MutationObserver` and, the first time it sees an identifier in
`data-controller`, dynamically `import()`s the matching file and registers it.

| `data-controller` | File |
|---|---|
| `nav` | `controllers/nav_controller.js` |
| `task-filter` | `controllers/task_filter_controller.js` |
| `pwa--install` | `controllers/pwa/install_controller.js` |

To add a controller, drop a `something_controller.js` file (with an
`export default class extends Controller`) into `web/static/js/controllers/` and
use `data-controller="something"`. There is no registry to update and nothing
to build. Controller URLs carry the asset version (`?v=`), so deploys bust
caches automatically.

`@hotwired/stimulus` is vendored at `web/static/js/vendor/stimulus.js` and mapped
with an import map, so there are no CDN calls and it works offline.

## Configuration

| Env var | Flag | Default | |
|---|---|---|---|
| `ADDR` | `-addr` | `:8080` | Listen address |
| `DB_PATH` | `-db` | `data/maintenance.db` | SQLite file (created with migrations applied on start) |
| `TZ` | | system | Facility time zone: decides what "today"/"overdue" means |
| `TRUST_PROXY=1` | `-trust-proxy` | off | Trust `X-Forwarded-For/Proto` from Caddy/nginx |
| `DEV=1` | `-dev` | off | Load templates/static from disk |

## CLI

```sh
maintenance-tracker create-user alice [--admin]   # prompts for password
maintenance-tracker reset-password alice
maintenance-tracker backup /path/backup.db        # safe while running
maintenance-tracker seed-demo
```

## Deploying

The app needs only the binary and the `.db` file. Use HTTPS in production: the
PWA install prompt and secure cookies require it.

**Docker (home-lab or droplet):**

```sh
docker compose up -d --build        # data lives in ./data/maintenance.db
```

**Plain binary + systemd** (e.g. a $4–6 Digital Ocean droplet):

```sh
make build-linux                    # or build-linux-arm for a Raspberry Pi
scp bin/maintenance-tracker-linux-amd64 server:/tmp/
# then follow the comments in deploy/maintenance-tracker.service
# and put deploy/Caddyfile in front for automatic HTTPS
```

### Moving between home-lab and Digital Ocean

1. Get a snapshot: **Settings → Download backup**, or `maintenance-tracker backup snap.db`.
2. On the new host, stop the app, copy the file to `DB_PATH`, start the app.

Copying the live `.db` while the app runs can miss data held in the `-wal`
file. Use the backup command/button, or stop the app first.

## Security notes

- Passwords are bcrypt-hashed. Sessions are random tokens stored server-side, in `HttpOnly`, `SameSite=Lax` cookies (`Secure` over HTTPS).
- CSRF uses a double-submit token on every POST. Logins are limited to 10 failures per IP per 15 minutes.
- A strict CSP (`script-src 'self'` + import-map hash) is set, along with `X-Frame-Options`, `nosniff` and HSTS over HTTPS.
- Changing a password signs out that user's other sessions.

## Layout

```
main.go, seed.go            entrypoint, CLI commands, demo data
internal/store/             SQLite access, migrations (migrations/*.sql), scheduling logic
internal/server/            routes, middleware, handlers, PWA endpoints
web/templates/              layout, partials, pages, sw.js (service worker)
web/static/                 css, js (application, autoloader, controllers, vendor), icons
deploy/                     systemd unit, Caddyfile
```

## Tests

```sh
make test
```

These cover the scheduling rules, sessions, CSRF, login rate limiting, and an
end-to-end flow that renders every page.

## License

MIT. See [LICENSE](LICENSE).
