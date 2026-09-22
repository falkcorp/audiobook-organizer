### Added

- A target-state design for the identification pipeline in
  `docs/architecture/identification-pipeline.md` (Part II): a per-file
  identification state machine built as an extension of `database.ScanState`
  rather than a new column beside it, a continuous low-priority driver that
  advances each file, and the reasoning for keeping the file spine linear while
  the book level stays a set of independent flags.
- The same document now records what items 3 and 4 actually did on production
  once they shipped — `itunes_tree` gone from the exclusion map (it read
  143,766), and `unknown_duration` down from 3,928 to 5 — so it reads as a live
  record rather than a stale plan.
