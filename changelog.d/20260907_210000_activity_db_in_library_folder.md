### Changed

- **The activity database now lives in the audiobook library folder by default**, at
  `<library>/.activity/activity.sqlite`, instead of sitting beside the main database. The two
  have very different storage needs: the activity log grows without bound — 29 GB on the
  production instance — while the main database sits on a small system volume. The dot prefix
  is what makes this safe inside the library tree, because every library walk already skips
  hidden directories; that is load-bearing rather than incidental (the skip rule deliberately
  carves out `.alternates`), so a test now pins it. With no library root configured it still
  falls back beside the main database.

### Added

- **The activity database can be relocated when its configured path changes.** A new setting
  decides whether an existing database follows the path or stays put; it defaults to following,
  because the alternative silently strands the history while a new empty log starts up.

  Moving a live multi-gigabyte SQLite file is not a copy of one file, and the implementation
  treats it that way. Committed rows can live in the `-wal` rather than the `.sqlite` — the
  state any killed process leaves behind, and this instance's activity store *was* OOM-killed —
  so a naive copy of the main file loses them, and on a crashed snapshot that file may not even
  carry the schema. Carrying the `-wal` across instead risks pairing it with a database it does
  not match. The move checkpoints first, copies the single `.sqlite`, and lets SQLite rebuild
  the sidecars.

  Source and destination are usually on different filesystems, so the move is a real copy, and
  it is a copy even within one filesystem: a rename destroys the original before anything has
  been checked. The copy is verified by comparing an actual row count against one taken before
  the move — a size comparison would pass on a file SQLite cannot open — and the original is
  removed only after that check succeeds. Every failure path leaves it intact, and a failed
  move never blocks startup. Progress is reported while a large copy runs.
