### Added

- Activity log gains a backend-agnostic SQL store (`internal/database/sql_activity_store.go`)
  with a dialect seam, implemented on the no-cgo `modernc.org/sqlite` driver. It
  is selected via the new `activity_backend` config (default `sqlite`; set
  `pebble` to roll back) with the file at `activity_db_path` (default
  `activity.sqlite` beside the main database).

### Changed

- Activity compaction is now structurally bounded on the SQL backend:
  `CompactByDay` reads only per-day aggregate counts plus at most 500 sample
  items and issues a set-based range delete, so the "Compact after N days"
  button can no longer time out no matter how large a single day is. Reads run
  from a WAL reader pool while the single writer compacts, so a long compaction
  no longer blocks the activity UI.

### Migration

- On boot the activity log dual-writes to both Pebble and SQLite; a background
  one-time backfill copies all Pebble history and, only after verifying per-tier
  parity, flips reads to SQLite. Every SQLite write is keyed by a deterministic
  content hash of the entry, so the backfill's copy of an event the live
  dual-write already stored is an idempotent no-op — there is no duplicate and
  no reliance on a timestamp cutoff (activity timestamps are caller-supplied and
  non-monotonic). Parity is verified by re-presentation: after copying a tier,
  re-inserting every scanned entry must insert zero rows. Reads stay on Pebble
  until parity passes, so the log is never served empty or half-migrated.
  Rollback is `activity_backend: pebble`.
