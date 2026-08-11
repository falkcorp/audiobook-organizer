### Fixed

- Every library organize spent 20-25 minutes on a pre-organize database backup
  that then failed, so from the UI the operation appeared never to start. The
  backup archived the **live** PebbleDB directory, so compaction deleted `.sst`
  and `.log` files between the directory walk enumerating them and the archiver
  reading them — measured twice on a 14 GB production database (20m28s and
  24m31s, both ending in `lstat .../536537.sst: no such file or directory`).
  Meanwhile the phase reported no progress at all, so the operations registry
  logged `strike recorded ... kind=stuck` against an operation that was working.
  The backup now uses PebbleDB's `Checkpoint` (flush + hard-link, consistent by
  construction), announces itself through the progress channel, and is skipped
  when a successful backup is already less than six hours old.
- The system backup handler resolved `Checkpointable` with a bare type
  assertion, which the search-index store decorator silently defeated — so the
  manual "create backup" button had quietly been taking the same racy
  live-directory archive. It now resolves the capability through the decorator
  chain and logs loudly when it cannot.
