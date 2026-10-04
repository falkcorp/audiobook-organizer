### Fixed

- Activity-log checkpoints no longer stall foreground writes while waiting for
  a reader. The SQLite activity store's checkpoint connection now runs with a
  busy timeout of 0, so a TRUNCATE checkpoint reports busy at once instead of
  holding the WAL write lock for up to 1 s per attempt. This covers both the
  post-vacuum truncate and the background idle-tick truncate. In review, a
  reader opened after a VACUUM had stalled activity writes for 2.5 s. They now
  take a few milliseconds.
