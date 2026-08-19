### Fixed

- **Deleting a book now removes its dedup candidates.** `DeleteBook` tore down the
  book's main row, signature sidecar, path/version-group/work/hash indexes,
  embedding row and chapters — but never the dedup candidates that referenced it.
  Only one of its sixteen call sites cleaned up after itself, so archive sweeps,
  reconcile, diagnostics, iTunes/FS regroup, batch operations and the audiobooks
  service each left candidates pointing at books that no longer exist. Those rows
  can never be actioned (clicking Merge returned "book not found") and can never
  be re-scored, because every producer iterates live books only — so they
  accumulated in the pending review queue permanently. A 2026-08-19
  `dedup.breakdown-backfill` dry run counted 2,504 of them. The teardown now lives
  in `DeleteBook` itself, so every caller is covered by construction, and the key
  list is shared with `DeleteCandidate` so the two cannot drift apart.
