### Fixed

#### `DeleteBookFilesForBook` no longer leaves stale memdb rows

The method deleted the Pebble rows and their indexes but never told memdb, so
memdb-backed reads (`total_file_count` among them) kept serving the deleted
files until a restart — invisible whenever the aggregate recompute happened to
resync the book as a side effect, and biting exactly when aggregates were
unchanged (the early-return path the 2026-08-03 canary hit). It now mirrors
`DeleteBookFilesByIDs`' derived-state pass: memdb delete + quick-query dirty
mark. The regression test reads through the memdb-dispatched getter with a
zero-aggregate fixture — both choices mutation-forced: the obvious
instrument (`GetBookFiles`) and a non-zero fixture each let the unfixed code
pass.
