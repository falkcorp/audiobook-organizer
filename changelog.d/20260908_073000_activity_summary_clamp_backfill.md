### Fixed

- **Reclaim the activity log's oversized `summary` history.** The 8 KiB summary cap
  (`activitySummaryMax`) is applied by the write path, so it bounded every new row and
  left the existing ones alone. On prod that history was the bulk of the database:
  measured 2026-09-07, `activity.sqlite` was 5.3 GB of which `summary` alone held
  5.48 GB — 208,103 `system` rows averaging 27 KB, the largest a single 9,558,930-byte
  iTunes write-back failure that formatted ~100k contract violations into one string.
  A new `POST /api/v1/activity/clamp-summaries` applies the same clamp retroactively.

  It defaults to a **dry run** — the pass rewrites historical rows, so measuring is the
  safe default and mutating requires `{"apply": true}`. `max` bounds a run and reports
  `truncated` so a capped run is distinguishable from a complete one; `vacuum` returns
  the freed pages to the filesystem (skipped for a dry run and when nothing was
  clamped, since VACUUM rewrites the whole database).

  The pass reuses `clampActivitySummary` rather than reimplementing it, so a backfilled
  row is byte-identical to a freshly-written one and the pass is idempotent,
  interruptible and resumable — cancelling returns committed progress instead of
  discarding it.

  **Clamped rather than compressed on purpose.** `summary` is searched with `instr()`
  and is TEXT, not BLOB; compressing it the way `details` is compressed would need a
  column-type migration and would break substring search, to save space on rows this
  bounds to 8 KiB anyway.
