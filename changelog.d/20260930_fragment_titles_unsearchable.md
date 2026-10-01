### Fixed

- The metadata candidate fetch no longer searches a book row that is one chapter file of a set the scanner filed as separate rows. Such a row is skipped outright, with no stand-in title, so a whole book's candidate is no longer attached to one chapter file. The skip applies to:
  - plain chapter numbers ("06 Chapter 6", "98"), including an empty or placeholder title ("Unknown Title", "Unknown", "Untitled") on a chapter-number file, beside sibling rows;
  - `_copyN` titles;
  - "Cobra 100 of 151" beside same-set counted siblings, matched on the row's own file name, so an author-prefixed, underscored or differently tagged file still finds its set;
  - "The Sunrise Lands 1" or "Sealed to the Flame E" beside stem siblings;
  - a folder name with rip details stamped onto chapter rows, judged by the same duration rule, so a box set's whole-book files are not skipped.
- Chapter numbers and `_copyN` are never exempted by duration. Counted and trailing-token titles are treated as whole products from 2 h. That line comes from prod: split parts run up to 1.5 h, and sold parts and whole books run 2.2–25 h. With no trustworthy duration, a set of 6+ rows is needed. A duration that could be milliseconds (2 h or more with no file size, or one rejected for its size) is not trusted, and the book's copy is not used in its place.
- Twin rows at one path are not siblings. Import paths and the library root are never listed, on the fetch and the apply paths alike.
- `force` on the batch candidate fetch and `?refresh=true` on the search dialog now re-ask the providers instead of replaying the per-source fetch cache (`SearchOptions.BypassFetchCache`).
- A title with an unspaced-colon subtitle ("In Fire Forged: Worlds of Honor V") is also searched by its main title, anchored on the author. The series' omnibus or box set is never accepted for it.

### Changed

- The batch candidate fetch's global rate gate is now the sum of the enabled sources' effective budgets (prod: 8 + 3 + 2 + 3 = 16/s) instead of a fixed 10/s. Its worker pool is sized from that budget within 16–32 instead of a fixed 8, and is never larger than the slowest source can drain within its timeout (rate × timeout × 0.5). `metadata_candidate_fetch_workers` overrides the pool size, under the same cap. The op logs the slowest source's rate as its expected throughput.
