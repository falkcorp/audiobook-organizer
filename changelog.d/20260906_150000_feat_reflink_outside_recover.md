### Added

#### `recover-missing-files` can now restore an out-of-tree source by reflink (`reflinkOutside` mode)

`maintenance.recover-missing-files` gained an opt-in `reflinkOutside` apply mode
(Branch B) that recovers a missing `book_file` row whose bytes exist only under a
`SourceDir` — the "outside" census bucket, ~54k rows on prod after a wide-source
census. For each such row that is bidirectionally unique (exactly one unclaimed
source file of the row's byte size and extension, wanted by exactly one missing
row), it clones the source (`fileops.ReflinkOrCopy`, copy-on-write where the
source and `RootDir` share a ZFS pool, a byte copy otherwise) back to the row's
**own** `FilePath`, so the row resolves again.

Because the row already points at that path, the recovery writes **no DB row and
does no repoint** — it only puts the bytes back where the row expects them. This
deletes the organizer coupling an earlier plan assumed (no exported
`EnsureUnderRoot`, no `BuildInTreeDestination` on `ServerDeps`); a local
`filepath.Rel` under-root check refuses a mangled/doubled destination path
instead, and `ReflinkOrCopy` refuses an existing destination, so a file some
other process already restored is skipped, never clobbered. The write phase runs
under the same scan stand-down the in-tree repoint uses (acquired once, renewed
per item, hard-abort on a lapsed lease), and re-stats the source immediately
before cloning. The mode is off by default: with `reflinkOutside` unset,
`recover-missing-files` behaves exactly as before (in-tree repoint plus census).
The op now declares `CapFilesRead`/`CapFilesWrite` for the clone. Reflinks are
bounded by the existing `max` param and taken in a stable file-ID order, so a
capped run creates a deterministic prefix. After a run, `mark-missing-files
apply=true` reconciles the Broken Files counter (it re-stats each restored path
and clears the `Missing` flag the counter reads).
