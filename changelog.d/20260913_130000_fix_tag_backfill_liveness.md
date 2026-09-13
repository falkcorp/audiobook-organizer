### Fixed

- **Tag backfill was killed as "stuck" while it was still working.** The op
  only reported progress when a whole book finished, and one book can hold over
  a thousand files, so a healthy run went five minutes without a progress stamp
  and the stuck-op watchdog canceled it at book 48,747 of 48,749, losing the
  whole dry-run result. The op now stamps its liveness after every file it
  reads (through a new `registry.TouchLiveness`, which changes no progress
  numbers and writes nothing to the database) and shows the book and file it is
  on. A single tag read is now bounded at 60 seconds: a read that hangs (TagLib
  WASM cannot be interrupted) counts as a read error for that file, is named in
  a WARN log, and the book continues; if 8 such reads are still stuck at once,
  the op fails with an error instead of piling up goroutines.
