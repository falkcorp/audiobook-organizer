### Fixed

- The reconcile package's book writes now go through `ModifyBook`, which sets
  only the columns each site owns on the stored row under the book's write
  lock. Until now each site read the book, did its work (a path stat, a
  segment stat storm, a metadata merge) and wrote the whole row back, so any
  column another writer committed in between was silently reverted (audit
  A1#15, lost-update race). Converted: the reconcile apply's `file_path`
  write; the version-group cleanup's kept/original `is_primary_version` writes,
  whose errors were discarded and are now logged and counted in a new
  `write_errors` result field; the broken-segment mark; the no-VG merge's
  soft-delete and its primary/keeper metadata writes (a snapshot-and-diff, so
  only the fields the merge filled are copied onto the live row); and the
  orphan version-group assignment, whose clobber guard now runs on the locked
  re-read. A book that vanishes between the read and the write is reported
  instead of dereferenced. Two lost-update tests prove a concurrent `Duration`
  write survives the two whole-library passes.
