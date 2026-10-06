### Fixed

- **Version twin fixer: no slow work under a book's write lock.** The
  apply's under-lock check looked up the record's metadata hash through the
  store's fallback, which scans every book row (~35 s on prod) whenever memdb
  is not serving, and resolved file paths on disk, both while holding the
  primary's write stripe. The slow checks (outside-group hash lookup, iTunes
  path resolution) now run immediately before the write, outside any lock.
  Under the lock the fixer reads the hash from memdb only
  (`PebbleStore.GetBooksByMetadataSourceHashInMemory`, never a scan), and
  refuses the row `changed_since_plan` when memdb is off, warming up or
  missing rows; a path the pre-write check did not clear is refused rather
  than resolved. The apply's `Guard` now also runs once on the row as read,
  before the apply body, so a refused row no longer leaves a new series row
  behind.
- **Version twin fixer: a matching narrator no longer outweighs a runtime
  gap.** Known runtimes more than 1% apart (under the 5% edition hold) with
  the primary carrying the record's narrator are now held
  (`edition_evidence_conflict`) instead of copying the record's ASIN. The
  narrator counts as edition evidence only when fewer than two runtimes are
  known.
- **Version twin fixer: a restart between the write and its journal row no
  longer escapes the operation revert.** The resumed run of the same operation
  recognises its own write (its history batch for an apply; the twin's copied
  cache for a candidate copy) and records the missing journal row once.
- **Version twin fixer: the outside-group identifier check covers ISBN and
  only what is copied.** ISBN-10 and ISBN-13 are checked as well as the
  ASIN, the ASIN lookup asks for upper- and lower-case spellings, and an
  identifier the apply will not copy (no edition evidence, or the primary
  already has one) no longer holds the row (`identifier_on_book_outside_group`).
