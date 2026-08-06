<!-- file: changelog.d/20260805_210000_linkintegrity_report.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2d84f0b1-9e57-4a63-8c1f-05b7ea63924d -->
<!-- last-edited: 2026-08-05 -->

### Added

- **`internal/linkintegrity` — shared report vocabulary for the "First Aid"
  library validate + repair pass.** Pure types and pure functions (no I/O):
  `Shape` (a book's `FilePath` resolving to a file, a directory, or nothing),
  `Disposition` (what to do about it), `Finding`, and a `Report` whose
  `Reconciles()` check guards the silent-filtering bug class that the existing
  maintenance ops' `RECONCILE` log lines were added to catch.

  Groundwork for sequencing four previously-disconnected maintenance ops plus a
  gap none of them covered. A whole-library survey on 2026-08-05 found **17,149
  of 44,886 books (38.2%) have zero `book_file` rows** — of those paths, 16,027
  resolve to a real file, 1,029 to a directory, and only 93 are genuinely
  missing. They are *unlinked*, not orphaned, so the remedy is to relink rather
  than delete. `maintenance.reconcile-scan` cannot see them: it flags a book only
  when `os.Stat` on its path *fails*, and these all stat fine.

  This also fixes an ordering bug rather than a packaging one.
  `maintenance.regroup-shattered-ai` derives `DurationSec` by summing a book's
  `book_file` rows, and its `membersAreBookLength` series-guard — the check that
  stops distinct novels being merged into one book — cannot fire when that sum is
  zero. With 97.5% of the review queue made of zero-file books, the guard was
  inert and the queue was built on blank evidence. Relink must run before
  regroup, and the package documents that constraint.

  `Disposition` deliberately has no `delete` value: deleting a redundant book row
  is not idempotent, because rescan regenerates a book for any file that no
  `book_file` row claims, so deleted rows return on the next scan. Duplicates are
  resolved by re-association instead.
