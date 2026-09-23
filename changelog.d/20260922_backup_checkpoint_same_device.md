### Fixed

- Auto-organize no longer dies in its pre-organize backup. The Pebble checkpoint
  was staged inside the backup directory, which production keeps on a different
  ZFS dataset from the database; hard links failed with EXDEV and Pebble silently
  byte-copied the whole ~14 GB database with no progress, so the registry
  watchdog cancelled the library scan the backup ran inside (5,790 books left
  unorganized on 2026-09-22). The checkpoint is now staged beside the database, a
  cross-device layout is refused in milliseconds instead of copied, orphaned
  checkpoints older than 24 h are swept (including legacy `pebble-checkpoint-*`
  dirs in the backup directory), and backup phase boundaries are always logged.
