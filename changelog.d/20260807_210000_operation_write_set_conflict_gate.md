<!-- file: changelog.d/20260807_210000_operation_write_set_conflict_gate.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7c2e5f9a-4b1d-4e8c-a3f6-9d0b2c5e8a14 -->
<!-- last-edited: 2026-08-07 -->

### Added

- Declared write-sets + scheduler conflict gate for operations. `OperationDef`
  gains `Writes []Resource` / `Reads []Resource` (table-level: books,
  book_files, authors, series, review_items, embeddings, operations), and the
  dispatcher's new Gate 3b refuses to START an op whose declared `Writes`
  overlap those of any currently running op — the op stays queued and
  dispatches when the conflict clears, with one deduplicated log line per
  deferral. Table-level granularity is deliberate: prod writes are whole-row
  read-modify-write (`UpdateBook` carries every field), so field-disjoint ops
  still lose fields when interleaved. Empty `Writes` = undeclared = gate
  skipped, so rollout is incremental. First adopters (today's incident pair
  plus siblings): `acoustid.backfill` (books, book_files),
  `maintenance.repair-transcribe-status` (books),
  `maintenance.intro-migrate-single-file` (book_files),
  `maintenance.transcribe-book-intros` (books).
