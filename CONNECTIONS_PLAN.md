# Connections as admin settings

The design is in `../event_planning/CONNECTIONS_PLAN.md`; read it first.
The event planner builds it first, so copy from there.

Status: planned, nothing built yet (2026-10-11).

Connections here: **Single sign-on**, and later the optional **Calendar**
read connection from `CALENDAR_PLAN.md`. The tracker is what the others
connect *to*, so its API tokens page stays as it is.

## Steps

### 1. Settings plumbing
- [ ] Copy `internal/secrets` and the connection type from the event planner
- [ ] Tests: whole-connection rule, blank secret keeps the old one

### 2. Single sign-on
- [ ] `/admin/connections` with the SSO section
- [ ] SSO client behind `atomic.Pointer`
- [ ] Pending values, **Test sign-in**, admin-role check, turn off with confirm, **Keep in settings**
- [ ] Saved-but-broken SSO falls back to local login with a banner
- [ ] `maintenance-tracker connection clear sso|calendar`
- [ ] README **Connections** section

### 3. Calendar (only with the optional part of `CALENDAR_PLAN.md`)
- [ ] Calendar read connection (address + read token) in the same page

### 4. Ansible
- [ ] Connection variables optional in `vars.yml`; backup includes `secrets.key`; README hand-over steps
