<!-- file: changelog.d/20260803_193000_narrator_all_tiers.md -->
<!-- version: 1.0.0 -->
<!-- guid: c41a7e92-58d3-4b06-9f27-6ad30e15b8c4 -->
<!-- last-edited: 2026-08-03 -->

### Fixed

- The Narrators tab showed only a handful of names. It was built from the
  `BookNarrator` junction alone, but for organized books the junction is nearly
  empty and the data lives in `Book.NarratorsJSON` or the legacy `Book.Narrator`
  column. Book detail already read all three tiers, so a book page showed a
  narrator the Narrators tab did not.

  The tab now uses the same three-tier resolution as book detail, sharing one
  definition of the precedence so the two cannot drift apart. `NarratorsJSON` was
  added to the `BookSummary` projection to keep this a single cheap pass rather
  than a per-book fetch.

  Measured on production: 69 of 120 sampled visible books (57.5%) have a narrator
  stored — roughly 9,500 of 16,491 — against 8 shown before this fix.
