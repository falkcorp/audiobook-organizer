<!-- file: changelog.d/20260807_120000_intro_tier0_migration.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3d8f60a4-9b27-4e15-8c03-71a5be2d94f6 -->
<!-- last-edited: 2026-08-07 -->

### Added

- **`maintenance.intro-migrate-single-file` — tier 0 of the per-file intro
  backfill.** Copies the book-level intro transcript onto the book's one
  `BookFile` row for the **33,780 single-file books (75.3% of the library)** at
  **zero GPU cost**. Storage landed in #2168 and the classifier in #2170, but no
  op wrote per-file transcripts until now.

  A full-library sweep on 2026-08-07 (all 44,875 books) sized the tiers exactly:

  | shape | books | % | files |
  |---|---|---|---|
  | 0 `book_file` rows | 1,122 | 2.5% | 0 |
  | 1 file — **this tier** | 33,780 | 75.3% | 33,780 |
  | 2–5 files | 2,884 | 6.4% | 7,829 |
  | 6–20 files | 2,775 | 6.2% | 34,601 |
  | 21+ files | 4,314 | 9.6% | 228,681 |
  | **total** | **44,875** | | **304,891** |

  **9.6% of books hold 75% of all files**, so tiering is what makes the backfill
  affordable rather than a nice-to-have: ~12–14 GPU-days naive versus ~1.4 days
  once this tier is free and the rest probe three files each.

  🔴 **Multi-file books are refused on provenance grounds, not skipped for
  convenience.** The book-level transcript does not record which file produced
  it, and the silence path retries against the *second* audio file — so copying
  onto file 1 would assert evidence the data cannot support. For a single-file
  book the ambiguity provably cannot arise (`nthAudioFile` returns `""` once
  `n >= len(audio)`, and the retry skips on an empty source). Books with zero
  `book_file` rows are reported as skipped, never counted as migrated: they are
  unlinked, not un-transcribed.

  The write guard: all mutation is isolated in one function whose permitted field
  set is declared as data, and a reflective test fills every `BookFile` field with
  a distinguishable value and fails if *any* field outside that set changes —
  verified to bite by injecting a rogue write. A companion test fails when a new
  transcription-shaped field is added to `BookFile` without deciding whether tier
  0 carries it, so schema growth cannot silently leave columns empty on 33,780
  migrated rows. Pages partition books into disjoint sets so no two workers touch
  the same row. `dry_run` defaults to true.
