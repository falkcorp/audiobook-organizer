<!-- file: todo.d/20260805_220500_relink_unlinked_books.md -->
<!-- version: 1.0.0 -->
<!-- guid: b52c7e04-a319-4d86-90f7-8e14036b2a97 -->
<!-- last-edited: 2026-08-05 -->

- [x] **Relink unlinked books — detector + repair op** — owner item 5
  (2026-08-05). Op `maintenance.relink-unlinked-books` shipped in PR #2147.

  **The measurement.** A whole-library survey found **17,149 of 44,887 books
  (38.2%)** own ZERO `book_file` rows — not the ~1,300 originally estimated.
  Disk check of every one of those paths: **16,027 resolve to a real file, 1,029
  to a directory, 93 are genuinely missing.** They are **unlinked, not orphaned**
  — the remedy is to relink, never to delete.

  **Why no existing op saw them.** `maintenance.reconcile-scan` flags a book only
  when `os.Stat` on its path FAILS. These all stat fine, so it walked past every
  one and reported the library healthy.

  🔴 **Why this blocked everything else.** `regroup-shattered-ai` derives
  `DurationSec` by summing `book_file` rows, and its `membersAreBookLength`
  series-guard — the check that stops distinct novels being merged — cannot fire
  when that sum is zero. With **97.5% of the review queue** made of these books,
  the guard was inert and the queue was built on blank evidence.

  ⚠️ **Do not measure this with `Book.duration`.** It is a snapshot and is
  populated (16,596 of the 17,149 have `duration > 0`), so coverage looks ~85%
  when the classifier's real coverage was ~2.5%. Measuring the wrong field is how
  this stayed invisible. `total_file_count` on the LIST DTO is a validated proxy
  (100% agreement vs per-book `/files` across 4,774 books); the single-book
  endpoint does not populate it.

- [ ] **Remaining after the first apply:** 1,019 directory-shaped books held for
  review (see [[first-aid-library-validate-repair]] tier-2 duration probe) and 93
  missing reported only (already `reconcile-scan`'s remit; some may be offline
  mounts rather than deleted audio).

- [ ] **Re-run `regroup-shattered-ai` after relink and re-measure the queue.**
  With durations present the series-guard becomes live for the first time across
  most of the queue. Baseline to compare against: 357 pending holds — 217
  ambiguous / 138 multidisc / 1 anthology / 1 version-group. This measurement
  tells us how much of owner item 1 was a DATA problem rather than a classifier
  problem, and should be taken before investing in recommendation tuning.
