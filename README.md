# Facility Maintenance Tracker

Track your sites, buildings, rooms and the equipment in them, with scheduled
and recurring maintenance. A public dashboard shows what's overdue and coming up; signed-in
users make changes. Installable as an app (PWA) on phones and desktops.

- **Backend:** Go, single static binary with templates/JS/CSS embedded
- **Database:** SQLite (one file, pure-Go driver with no CGO)
- **Frontend:** server-rendered HTML + [Stimulus](https://stimulus.hotwired.dev) 3.2 with a custom
  autoloader. No Node, no bundler, no Turbo/Hotwire.

## Quick start

Needs Go 1.27 or newer.

```sh
make dev                     # http://127.0.0.1:8080, edits to web/ show on refresh
```

The first visit takes you to `/setup` to create the admin account. To try it
with sample data (added to `data/maintenance.db`):

```sh
go run . seed-demo
```

To run in production, see [Deploying](#deploying).

## Features

| Area | What it does |
|------|--------------|
| Dashboard (`/`, public) | Status tiles (Overdue / Due soon / Upcoming / Not scheduled), per-building health, search and status filters (kept in the URL hash), and a filter for any building, floor or area. Works offline once visited. |
| Places | Sites hold buildings. Buildings hold floors, rooms and areas. Floors hold rooms and areas; rooms hold areas; areas hold floors, rooms and other areas, and can also go straight on a site. So floors are optional, outside spaces (a porch, a playground) are areas, a wing is an area holding rooms, and a stage is an area in a room. A shared area, like a stairwell between two floors or a balcony off two rooms, lives in one place (which gives it its name) and is also in the others: it's listed in each, and its problems and items count there, once. With one site (the usual case) it's created for you and never asked about. A place's page shows what's in it and rolls up problems, supplies and tasks from everything inside; moving a place takes its contents along; deleting one moves its contents up a level (it says so if something inside can't go there, like a room in an area on the site). Optional tags (e.g. "classrooms", "media") group places across buildings, with a page per tag and tag filters on Items, Supplies and Problems. Add places one at a time or paste a list. Links and QR codes from before places (`/rooms/3`, `/report?room=3`) still work. |
| Items | Items belong to a place at any level (a mailbox on the site, AC units on a building, lights on a porch). An item can count a group of identical things (10 outlets, 15 lights) and use a supply that gets replaced (1 bulb or filter each); "Replace" records it and takes it from stock. A group's units are numbered #1, #2… (with optional location notes) so Replace, "Mark done" and other work can record which ones. Every unit also has an ID, what's on its sticker, which stays with it through moves even when its number changes. IDs start out as the unit's number and can be changed when the item is added or later. They're unique within a kind: all portable items of the same product share one list, so Lapel mics and Handheld mics can each have a 1, and a second set of Lapel mics carries on from the first; anything that isn't portable has a list of its own. Units replaced 3 or more times in a year are flagged. Portable items (projectors, TV carts, chairs) have "Move", and every change of location is kept in their history. A group can move only some of its units (tick which ones, or say how many): they join a matching item already at the new place, or become a new one there with copies of its tasks, and those left behind are renumbered from #1, keeping their IDs, notes, history and problems. Place pages group items by category. |
| Products | An item's name picks its product, so the same thing in many places adds up: 40 folding chairs in the hall and 60 in the gym are one product, Folding chairs, with 100 in all. The Products page totals each one, everywhere or in one place or tag. A product holds the category, notes and how its items are kept: one by one (numbered units with IDs, as above) or only counted, for things like chairs and tables that don't need numbers of their own. Counted items move by count and have no unit pickers. Rename a product, or switch how it's kept, from its page; two products that are really one thing can be merged. Products are added and removed with their items. |
| Tasks | One-time or recurring (every N days/weeks/months/years). "Next due" is calculated from "last done" or set by hand. Month math clamps (Jan 31 + 1 month = Feb 28). |
| Mark done | Logs date, who, cost and notes, then rolls the task forward. Back-dated entries never move the schedule backwards. One-time tasks close. |
| History | Per-item and site-wide maintenance logs, including ad-hoc "other work". |
| Supplies | Counts of things that get used up (paper towels, filters, bulbs) or washed and reused (mop heads), with a history of every change and low-stock flags. Tasks can take a supply each time they're done. |
| Problems | Report a problem against a place or an item in it; staff can narrow it to one numbered unit, mark "I'm on it", assign, add notes, and record the fix (Replace or Log work) straight from the problem, which resolves it. Only signed-in users can see reports. Admins can let anyone report without signing in, separately for places and for items (both off by default; 10 reports an hour per device, with a honeypot field for bots); otherwise scanning a code asks people to sign in first. Any place's page prints a QR code that opens the report form for it, or a sheet of codes for everything inside it. |
| QR codes and scanning | Items print stickers from their page (or a product's, for every one of it): one per unit, carrying the ID on its sticker, so scanning reports a problem with that exact one and still finds it after it moves; counted and plain fixed items get one code each. Supplies print a code for their shelf or bin (one, or a sheet for the supplies listed) that opens the supply's page to take some, recount, restock or "Ask for more". Asking flags it on the dashboard and Supplies page until it's restocked. Signed-in users can scan any of these from **Scan** with the phone's camera (the browser's QR reader where there is one, otherwise the bundled [jsQR](https://github.com/cozmo/jsQR), Apache 2.0); the camera needs https. A phone's own camera app works too. |
| Live updates | The dashboard, Problems and Supplies pages, and each problem and supply, update by themselves while open when anyone saves a change (over [Server-Sent Events](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events) at `/events`). If you're partway through a form there, a notice offers the changes instead of wiping what you typed. An installed app on a phone catches up when it comes back to the screen; phones pause apps in the background, so this doesn't notify anyone. |
| Users | Admins manage users, settings (site name, site address, "due soon" window, public problem reports) and can download a backup. Editors manage data. |

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

Set with a flag or an environment variable. Flags go **before** any command
(`maintenance-tracker -db /path/x.db create-user alice`).

| Env var | Flag | Default | |
|---|---|---|---|
| `ADDR` | `-addr` | `:8080` | Listen address |
| `DB_PATH` | `-db` | `data/maintenance.db` | SQLite file, relative to the working directory. Created, with migrations applied, on start; its folder must be writable. |
| `TZ` | | system | Facility time zone (e.g. `America/Chicago`): decides what "today" and "overdue" mean |
| `TRUST_PROXY=1` | `-trust-proxy` | off | Trust `X-Forwarded-For/Proto` from a reverse proxy, for secure cookies, HSTS and real client IPs. Only turn on when a proxy is the only way in; otherwise clients can fake their IP. |
| `DEV=1` | `-dev` | off | Load templates/static from `./web` on disk (development only) |

Everything else is in the app under **Settings** (admins): site name, **site
address** (the public `https://…` address used in links and QR codes, set it
when running behind a proxy), the "due soon" window, and whether anyone can
report problems without signing in.

## Commands

```sh
maintenance-tracker create-user alice [--admin]   # prompts for a password (10+ characters)
maintenance-tracker reset-password alice
maintenance-tracker backup /path/backup.db        # safe while the app is running
maintenance-tracker seed-demo                     # sample places, items and tasks
```

They use the same database as the server, so pass the same `-db` (or `DB_PATH`)
and run them as the same user. The Docker and systemd sections below show how.

## Deploying

The app is one binary plus one `.db` file. Use HTTPS in production (secure
cookies and the PWA install prompt need it): put it behind a reverse proxy such
as [Caddy](https://caddyserver.com), which gets certificates automatically
(see `deploy/Caddyfile`).

Proxies must pass `/events` through unbuffered. Caddy does once it's left out
of `encode`, as in `deploy/Caddyfile`. With nginx, turn off `proxy_buffering`
for that path (the app also sends `X-Accel-Buffering: no`).

Until the first account exists, `/setup` lets **anyone** create the admin. On a
server reachable from the internet, create the admin with `create-user` before
opening it up, or visit `/setup` straight away.

After the first sign-in, open **Settings** and set the **Site address** (e.g.
`https://maintenance.example.org`) so links and QR codes point to the right
place.

### Docker

Build the image and start it with Compose. The container runs as user `10001`,
which must own the data folder:

```sh
mkdir -p data && sudo chown 10001:10001 data
docker compose up -d --build           # http://<host>:8080
docker compose exec app maintenance-tracker create-user alice --admin
```

Edit `TZ` in `docker-compose.yml` first. All state is in `./data/maintenance.db`.

Without Compose (after the same `mkdir`/`chown`):

```sh
docker build -t maintenance-tracker .
docker run -d --name maintenance-tracker --restart unless-stopped \
  -p 8080:8080 -e TZ=America/Chicago -v "$PWD/data:/data" maintenance-tracker
docker exec -it maintenance-tracker maintenance-tracker create-user alice --admin
```

Inside the container `DB_PATH` is already `/data/maintenance.db`, so commands
need no `-db`. Keep the app on port 8080 inside the container (the health check
uses it) and change only the published port.

Behind Caddy or nginx on the same machine, publish only to it
(`"127.0.0.1:8080:8080"`) and set `TRUST_PROXY: "1"`; both are commented in
`docker-compose.yml`.

| Task | Command |
|---|---|
| Logs | `docker compose logs -f` |
| Backup | **Settings → Download backup**, or `docker compose exec app maintenance-tracker backup /data/backup.db` (lands in `./data`) |
| Upgrade | `git pull && docker compose up -d --build` (migrations run on start) |

### Binary + systemd

For a Linux server or droplet without Docker. Build on your machine, copy the
binary and the files in `deploy/` over, then follow the comments at the top of
`deploy/maintenance-tracker.service`:

```sh
make build-linux                       # bin/maintenance-tracker-linux-amd64
# make build-linux-arm                 # 64-bit ARM, e.g. Raspberry Pi 4/5
scp bin/maintenance-tracker-linux-amd64 deploy/maintenance-tracker.service deploy/Caddyfile server:
```

On the server:

```sh
sudo useradd --system --home-dir /var/lib/maintenance-tracker --shell /usr/sbin/nologin maintenance
sudo install -m 755 maintenance-tracker-linux-amd64 /usr/local/bin/maintenance-tracker
sudo cp maintenance-tracker.service /etc/systemd/system/     # edit TZ first
sudo systemctl daemon-reload && sudo systemctl enable --now maintenance-tracker
sudo -u maintenance maintenance-tracker -db /var/lib/maintenance-tracker/maintenance.db create-user alice --admin
```

The service listens on `127.0.0.1:8080` only, with `TRUST_PROXY=1`, so install
Caddy and use `deploy/Caddyfile` (change the domain) to serve it over HTTPS.
The database lives in `/var/lib/maintenance-tracker/`.

Run commands with `sudo -u maintenance` and the `-db` path above. Running them
as root leaves files the service can't write to.

| Task | Command |
|---|---|
| Logs | `journalctl -u maintenance-tracker -f` |
| Backup | **Settings → Download backup**, or `sudo -u maintenance maintenance-tracker -db /var/lib/maintenance-tracker/maintenance.db backup /var/lib/maintenance-tracker/backup.db` |
| Upgrade | Copy the new binary, `sudo install` it as above, `sudo systemctl restart maintenance-tracker` (migrations run on start) |

### DigitalOcean with Ansible

`deploy/ansible/` automates the binary + systemd setup on a DigitalOcean
droplet. Nothing private is committed: the API token comes from the
environment, the droplet is found by its tag on every run, and your domain
and settings go in the gitignored `deploy/ansible/vars.yml`.

You need Ansible on your machine (`brew install ansible` or
`pipx install ansible-core`), Go, a DigitalOcean API token with read/write
scope, and your domain added under **Networking → Domains** in DigitalOcean
with the registrar's nameservers pointed at it.

```sh
cp deploy/ansible/vars.example.yml deploy/ansible/vars.yml   # set app_domain and dns_zone
export DIGITALOCEAN_TOKEN=...          # e.g. $(security find-generic-password -s digitalocean -w)
make provision                         # first time
make deploy                            # every update after that
```

`make provision` uploads your SSH public key to the account, creates the
droplet (Ubuntu 26.04, $6 size in New York 3 by default, weekly droplet backups on) in your chosen project, a cloud
firewall allowing only SSH, HTTP and HTTPS, and the DNS A record. It then
installs updates, Caddy and the service user, and runs the deploy. Running it
again leaves existing resources alone.

`make deploy` runs the tests, builds `bin/maintenance-tracker-linux-amd64`
here, and installs it with `deploy/maintenance-tracker.service` and a Caddy
site for your domain. When the binary changes, it backs up the database to
`/var/lib/maintenance-tracker/backups/` first and keeps the last 10 backups.
The deploy fails if the app doesn't answer `/healthz` afterwards.

The droplet can also host the
[production planner](../production_planning): its deploy finds this droplet by
its tag and adds itself alongside, with its own Caddy site in
`/etc/caddy/sites/production-planner.caddy`. Caddy's config is split so both
apps can deploy without overwriting each other: `/etc/caddy/Caddyfile` only
imports `/etc/caddy/sites/*.caddy`, and each app writes its own site file
there (this one's is `/etc/caddy/sites/maintenance-tracker.caddy`). Both repos
write the same main Caddyfile, so it doesn't matter which deploys last.

On the first deploy, the admin user is created before the app starts, so
`/setup` is never exposed. Set `MT_ADMIN_PASSWORD` to choose its password, or
leave it unset and a random one is printed at the end. Then sign in and set
**Settings → Site address**.

Every setting and its default (region, size, time zone, which addresses can
use SSH, …) is in `deploy/ansible/defaults.yml`; override any of them in
`vars.yml`.

Ansible can't answer a PIN prompt, so if your SSH key is on a hardware token
(e.g. a YubiKey) the plays hang at "Wait for SSH". Make a deploy key just for
this droplet, keep its passphrase in the macOS Keychain, and set
`ssh_private_key_file` in `vars.yml`:

```sh
ssh-keygen -t ed25519 -f ~/.ssh/maintenance_deploy -C maintenance-deploy
/usr/bin/ssh-add --apple-use-keychain ~/.ssh/maintenance_deploy
```

The keychain flags exist only in macOS's own `ssh-add`, hence the full path
(Homebrew's OpenSSH, often installed for hardware keys, comes first on the
PATH). After a restart, `/usr/bin/ssh-add --apple-load-keychain` puts the key
back in the agent without asking for the passphrase.

The droplet only gets the deploy key, so to keep logging in by hand with the
hardware key, list its public key in `ssh_authorized_keys` in `vars.yml`
(e.g. `[~/.ssh/id_ed25519_sk.pub]`). `make provision` adds them for root.

To have your own cloud firewalls cover the droplet, list their tags in
`do_extra_tags` and set `do_firewall: false`. Pass `-e skip_tests=true` to `ansible-playbook` to skip the tests.
To roll back, check out the earlier commit and `make deploy` it. If its
migrations changed the database, also restore the pre-deploy backup (see
[Moving between hosts](#moving-between-hosts)). SSH in as `root@<domain>` for
logs and commands, as in the systemd section above.

### Binary, run by hand

On any machine with the binary (`make build` builds one for the current OS):

```sh
./bin/maintenance-tracker              # http://localhost:8080, database in ./data/
./bin/maintenance-tracker -addr 127.0.0.1:9000 -db /srv/maint/maintenance.db
```

### Moving between hosts

1. Get a snapshot: **Settings → Download backup**, or the `backup` command.
2. On the new host, stop the app, copy the file to its database path, start the app.
   - Docker: copy it to `./data/maintenance.db`, then `sudo chown 10001:10001 data/maintenance.db`.
   - systemd: copy it to `/var/lib/maintenance-tracker/maintenance.db`, then `sudo chown maintenance: /var/lib/maintenance-tracker/maintenance.db`.

Copying the live `.db` while the app runs can miss data held in the `-wal`
file. Use the backup command or button, or stop the app first.

## API

Other apps, like an event or production planner, can read what there is and
where. An admin makes a token per app on the **API** page (it's shown once),
and the app sends it with each request:

```sh
curl -H "Authorization: Bearer mt_…" https://maintenance.example.org/api/v1/products?tag=events&portable=1
```

Everything is read-only JSON under `/api/v1/`:

| Endpoint | Returns |
|----------|---------|
| `GET /products` | Products with `total`, `item_count`, `place_count`. Filters: `q`, `category`, `place`, `tag`, `portable=1`, `counted=1`; a place or tag totals only what's there. |
| `GET /products/{id}` | A product and its `items`, each with its place. |
| `GET /items` | Items. Filters: `q`, `place` (with `direct=1` for only that place, not inside it), `tag`, `product`, `portable=1`. |
| `GET /items/{id}` | An item, with its `units` (number, ID, note) unless it's counted. |
| `GET /supplies`, `GET /supplies/{id}` | Supplies with `on_hand`, `in_use`, `cleaning`, `total`, `stock` (`ok`, `low`, `out`) and `requested` (when someone asked for more, or `null`). Filters as for items, plus `low=1` (low, out or asked for). |
| `GET /places`, `GET /places/{id}` | Places in tree order (`parent_id`, `kind`, `path`, `tags`, `also_in`); one place adds its `children` and the `items` and `supplies` in it (or `direct=1`). |

Errors come back as `{"error": "…"}`: 401 for a missing or revoked token, 400
for a place that doesn't exist, 404 for anything else not found.

## Security notes

- Passwords are bcrypt-hashed. Sessions are random tokens stored server-side, in `HttpOnly`, `SameSite=Lax` cookies (`Secure` over HTTPS).
- CSRF uses a double-submit token on every POST. Logins are limited to 10 failures per IP per 15 minutes.
- A strict CSP (`script-src 'self'` + import-map hash) is set, along with `X-Frame-Options`, `nosniff` and HSTS over HTTPS.
- Changing a password signs out that user's other sessions.
- API tokens are stored as SHA-256 hashes and can only read. The API takes only tokens, never session cookies, so it needs no CSRF check.

## Layout

```
main.go, seed.go            entrypoint, CLI commands, demo data
internal/store/             SQLite access, migrations (migrations/*.sql), scheduling logic
internal/server/            routes, middleware, handlers, PWA endpoints
web/templates/              layout, partials, pages, sw.js (service worker)
web/static/                 css, js (application, autoloader, controllers, vendor), icons
deploy/                     systemd unit, Caddyfile, ansible/ (DigitalOcean provision + deploy)
```

## Tests

```sh
make test
```

These cover the scheduling rules, sessions, CSRF, login rate limiting,
supplies, counted items and units, products (merging, switching to counted,
the migration), moves, the API and its tokens, problem reports (including public
reporting and its rate limit), site address and QR codes, and an end-to-end flow
that renders every page.

## License

MIT. See [LICENSE](LICENSE).
