<!-- file: changelog.d/20260802_080000_abs_stats_endpoints.md -->
<!-- version: 1.0.0 -->
<!-- guid: f3c5fecb-63ea-4028-a4b4-22fe634c19d7 -->
<!-- last-edited: 2026-08-02 -->

### Fixed

- **The app's connection indicator "turned orange randomly" because our deliberate
  404s flipped it.** AudioBooth's `NetworkService` sets the server status on every
  response — `guard 200...299 … else { updateStatus(.connectionError) }`, then
  `updateStatus(.connected)` — and `.connectionError` is the orange dot on the home
  screen. `/api/me/listening-stats` is fetched on every home-screen refresh and
  answered 404 by design, so the dot flipped orange and back on every refresh.

  Spec §1.8.6's reasoning ("callers use `try?`, so 404 is safe") was wrong twice:
  `try?` swallows the *error* but the status side-effect already fired, and
  `ListeningStats` has **four** required fields, not "~12".

### Added

- **Four listening-statistics endpoints**, all 200 with shape-complete, **truthful**
  bodies: `GET /api/me/listening-stats`, `/api/me/listening-sessions`,
  `/api/me/stats/year/:year`, `/api/me/item/listening-sessions/:id`.

  `listening-stats` reports a real `totalTime`, summed from the per-book listened
  totals the playback sync maintains. The per-day breakdowns are emitted **empty
  rather than fabricated** — attributing a book's whole total to its last-activity
  date would put invented numbers on the user's stats screen — and the session
  histories are empty because play sessions are in-memory by design, which makes an
  empty history the correct answer rather than a stub.

  A store failure reports zeros with a 200 rather than a 5xx: a 5xx trips the same
  indicator these endpoints exist to keep green.

  ⚠️ Covers are deliberately **not** changed: the client builds cover URLs directly
  for Nuke/AsyncImage rather than through `NetworkService`, so the ~80% of books with
  no cover art cannot trip the indicator. `GET /api/items/:id/cover` → 404 stays.
