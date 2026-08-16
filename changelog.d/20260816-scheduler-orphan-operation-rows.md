### Fixed

- **Scheduled tasks left one permanently-`pending` operation row per tick.** Five
  tasks (`scan`, `organize`, `library-size-refresh`, `acoustid-online-lookup`,
  `ai-dedup-batch`) created a legacy `operations` row under one ULID and then
  enqueued the *real* v2 operation under a **different** ULID. Nothing linked the
  two and nothing ever updated the legacy row, so it sat at `pending` forever.

  Measured against production on 2026-08-16, `GET /api/v1/operations` reported
  **183 of 200 rows pending**, some six days old, while the v2 record for the same
  window showed 179 completed, 13 interrupted_dropped, 6 canceled and 1 failed.

  The same bug also made the scheduler's "started operation" log line — and the
  `POST /tasks/:name/run` response — report an id that **no endpoint could resolve**,
  because the operation that actually exists carries the other id. Both now report
  the v2 id, which resolves via `GET /operations/v2/:id`.
