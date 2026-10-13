# Connections as admin settings

The design is in `../event_planning/CONNECTIONS_PLAN.md`; read it first.
The event planner builds it first, so copy from there.

Status: built (2026-10-12).

Connections here: **Single sign-on**, and later the optional **Calendar**
read connection from `CALENDAR_PLAN.md`. The tracker is what the others
connect *to*, so its API tokens page stays as it is.

## Steps

### 1. Settings plumbing
- [x] Copy `internal/secrets` and the connection type from the event planner
  (secret settings: `sso_client_secret`, `sso_pending_client_secret`, `calendar_token`)
- [x] Tests: whole-connection rule, blank secret keeps the old one

### 2. Single sign-on
- [x] `/admin/connections` with the SSO section
- [x] SSO client behind `atomic.Pointer`
- [x] Pending values, **Test sign-in**, admin-role check, turn off with confirm, **Keep in settings**
- [x] Saved-but-broken SSO falls back to local login with a banner
- [x] `maintenance-tracker connection clear sso|calendar`
- [x] README **Connections** section

### 3. Calendar (only with the optional part of `CALENDAR_PLAN.md`)
- [x] Calendar read connection (address + read token) in the same page

### 4. Ansible
- [x] Connection variables optional in `vars.yml`; backup includes `secrets.key`; README hand-over steps
  - `BASE_URL` is now only written alongside `sso_url`: on its own it would take the whole
    SSO connection out of the app's hands (whole-connection rule). Calendar token comes from
    `MT_CALENDAR_TOKEN`; the deploy stops if `calendar_url` is set without it.
