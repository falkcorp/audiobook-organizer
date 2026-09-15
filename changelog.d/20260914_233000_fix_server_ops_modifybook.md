### Fixed

- Entity, cover, scheduler, reconcile and AcoustID book writes now go through
  `ModifyBook`, which sets only the columns each site owns on the stored row
  under the book's write lock. Until now each of these paths read the book,
  did its work (a transcode, a cover-file restore, a join rewrite, signature
  synthesis) and wrote the whole row back, so any column another writer
  committed in between was silently reverted (audit A1#15, lost-update race).
  Converted: the author-merge `AuthorID` sync and the resolve-production-author
  publisher and author writes (`entities_ops.go`); the transcode op's demote of
  the original and its in-place fallback rewrite (`library_core_ops.go`); the
  cover-history restore's `cover_url` write; the scheduler author split's
  primary-author write, whose errors were discarded and are now logged and
  counted in the op's error total; `ElectMissingPrimaries`' election write; and
  the AcoustID book-signature write. A book that vanishes between the read and
  the write is now reported (not found) instead of dereferenced. One
  lost-update test per package proves a concurrent `Duration` write survives
  each path.
