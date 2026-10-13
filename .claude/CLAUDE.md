# Maintenance Tracker: project notes

- Every app must run alone. Links to other apps are optional and off
  unless their URL and token are set (the `Configured()` pattern); when
  off, the app acts as if the other app doesn't exist. Each app owns its
  own data; others keep only a thin copy or a reference. No hub app that
  everything depends on.
- Shared kinds of data (places/rooms, items, events, users) keep the same
  shape in every app, matching the owning app's API. A thin local version
  is a subset of the full one, so when the owning app is connected the
  local rows start referencing it (its ID plus a cached name) instead of
  being rebuilt.
