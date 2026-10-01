### Fixed

- The metadata candidate fetch no longer searches a book row that is one chapter file of a set the scanner filed as separate rows. Such a row is skipped outright, with no stand-in title, so a whole book's candidate is no longer attached to one chapter file. The skip applies to:
  - plain chapter numbers ("06 Chapter 6", "98"), including an empty or placeholder title on a chapter-number file, beside sibling rows;
  - `_copyN` titles;
  - "Cobra 100 of 151" beside same-set counted siblings;
  - "The Sunrise Lands 1" or "Sealed to the Flame E" beside stem siblings;
  - a folder name with rip details stamped onto chapter rows.
- Chapter numbers and `_copyN` are never exempted by duration. Counted and trailing-token titles are treated as whole products from 2 h. That line comes from prod: split parts run up to 1.5 h, and sold parts and whole books run 2.2–25 h. With no trustworthy duration, a set of 6+ rows is needed.
- Twin rows at one path are not siblings. Import paths and the library root are never listed, on the fetch and the apply paths alike.
- `force` on the batch candidate fetch and `?refresh=true` on the search dialog now re-ask the providers instead of replaying the per-source fetch cache (`SearchOptions.BypassFetchCache`).
- A title with an unspaced-colon subtitle ("In Fire Forged: Worlds of Honor V") is also searched by its main title, anchored on the author. The series' omnibus or box set is never accepted for it.

### Changed

- The batch candidate fetch's global rate gate is now the sum of the enabled sources' effective budgets (prod: 8 + 3 + 2 + 3 = 16/s) instead of a fixed 10/s. Its worker pool is sized from that budget within 16–32 instead of a fixed 8. `metadata_candidate_fetch_workers` overrides the pool size.
