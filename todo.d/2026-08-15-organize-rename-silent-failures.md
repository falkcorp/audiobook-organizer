## Organize/apply rename paths: three hand-verified silent failures

Found while unifying the target-path builders (`refactor/unify-path-builders`).
All three were read at the source and confirmed by hand — this is the
hand-verified count, not a machine-flagged count. None is fixed; the unification
PR deliberately carried only the two defects that were the stated requirement
(directory organize was not file aware; organized `book_file` rows were derived
by guessing rather than from the planner).

- [ ] **F7 — `ApplyMetadataFileIO` returns nothing, so rename failure is
      unreachable to every caller.** `internal/metafetch/service_files.go:80`:
      `func (mfs *Service) ApplyMetadataFileIO(id string)` has no return value.
      A failure from the apply pipeline is swallowed into
      `slog.Warn("apply pipeline failed for", ...)`. Six call sites cannot
      observe it, and `internal/server/batch_apply_one.go:124` reports
      `Applied: true` regardless of what happened on disk. This is the one that
      most directly breaks "updates all the rows correctly": the API says the
      apply succeeded when the files never moved. Fix is mechanical but wide —
      an `error` return, two interfaces (`internal/server/batch_apply_one.go:29`,
      `internal/server/handlers/metadata_cache.go:61`), six call sites and two
      regenerated mock files — which is why it was split out.

- [ ] **F6 — `ensureLibraryCopy` treats an empty organize as success.**
      `internal/metafetch/service_apply.go:~349`: `newBookPath = targetDir` is
      set unconditionally after `OrganizeBookDirectory`, and
      `OrganizeBookDirectory` `MkdirAll`s that directory before copying
      anything. If every source file is absent from disk, the copies all skip,
      `pathMap` comes back empty, and a new primary book record is created
      pointing at an empty directory. Partially mitigated by the unification
      PR — `OrganizeBookDirectory` now errors when every row is flagged
      `Missing` — but rows that are *not* flagged and are simply gone from disk
      still produce the empty-directory outcome. Check `len(pathMap)` at the
      call site.

- [ ] **F5 (remainder) — `OrganizeBookDirectory` still cannot tell a resumed
      copy from a stranger.** The unification PR narrowed this: a destination
      that already exists is now adopted only when it is `os.SameFile` with the
      source or byte-identical in size, and an unrelated occupant is warned
      about and left alone instead of being written into `pathMap`. The size
      comparison is still a heuristic — two different files of equal size are
      adopted as the same file. A content hash (the codebase already has
      `scanner.ComputeFileHash`) would settle it, at the cost of reading both
      files.

**Also worth doing:** `MoveBookFile` (`internal/organizer/move.go:32-98`) is the
one function in the repo with the correct pattern — verify source, verify
destination absent, move, DB-update, and **roll the file back if the DB write
fails**. It is on none of the three rename paths. Routing them through it would
retire most of the above rather than patching each.
