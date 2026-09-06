### Scan stand-down: Branch B + retrofit existing applies (follow-up to the control PR)

The scan stand-down control (registry `AcquireScanStandDown`) has landed with no
caller. Follow-up work, in one PR:

- **Branch B op** — reflink an outside source file into the in-tree destination
  (`fileops.ReflinkOrCopy`, cheap on ZFS block cloning), gated on
  `HasResolvedAuthor` + `ensureUnderRoot`, then repoint the `outside`-census
  `book_file` rows (full-record, `FilePath` only) with the
  `missing_file_repoint` claimed/collision guards. Re-apply bidirectional
  size+ext uniqueness at apply time. Declare `CapFilesWrite` + `CapLibraryWrite`;
  `RunItems` pool disjoint by row id. Wire a `ScanController` method onto
  `ServerDeps` (its first caller) and have the op acquire the stand-down for the
  apply run, checking `ScanStandDownValid` per batch and aborting on a lapsed
  lease.
- **Retrofit** `recover-missing-files`, `missing-file-repoint`, and
  `mark-missing-files` to acquire the stand-down for their apply runs, replacing
  the "don't run while a scan is active" precondition in their doc comments.
