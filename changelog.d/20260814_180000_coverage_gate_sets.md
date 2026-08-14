### Fixed

- The boot-time search-index coverage gate now compares ID SETS instead of
  counts: live books missing from the index are marked for re-index (as
  before), and stale documents whose book is gone — hard- or soft-deleted —
  are removed. The old `len(books) <= DocCount()` gate reported "coverage OK"
  on a production index padded with 3,953 soft-deleted books, and padding
  could equally hide genuinely missing live books. Adds
  `BleveIndex.AllDocIDs()` (one sequential low-level doc-ID pass).
