- [ ] **01-P81I-PREVIEWREPORT** Bring back a way to read the bulk-apply dry-run
      report. 01-P81i retired `GET /api/v1/metadata/bulk-apply-preview/:id`, the
      only reader of the per-book rows the `metadata.bulk-apply-preview` op writes
      (candidate, gate verdict, field changes, rename, plus a summary by verdict
      and reason, paging, filters and a download). The dry run is still the
      default of `POST /api/v1/metadata/batch-apply-candidates` and
      `POST /api/v1/audiobooks/metadata/batch-apply-cached`, and the op still
      writes the rows and an `_sibling_index` row, but nothing can read them over
      HTTP any more. What is left is the one-line summary in the finished
      operation's message (`GET /api/v1/operations/v2/:id`). Decide whether to add
      a reader (for example a typed results view on the operations API, which
      needs `GET /operations/:id/results` to accept this op, today it only
      resolves candidate-fetch operations) or to drop the per-book rows.
- [ ] **01-P81I-COVERRESTORE** Decide how to restore an archived cover. 01-P81i
      retired `GET /api/v1/audiobooks/:id/cover-history` and
      `POST /api/v1/audiobooks/:id/cover-history/restore`. The web UI never had a
      restore button, but cover embedding still archives the previous cover under
      `covers/history/{bookID}/` and nothing can list or restore those files over
      HTTP now. Either add a restore action to the book detail page (and a route
      that records an undoable metadata change) or stop archiving.
- [ ] **01-P81I-COLLISION** Decide whether import needs a pre-import collision
      check. 01-P81i retired `POST /api/v1/import/collision-preview`, which told a
      caller whether a file would collide with an existing book by torrent
      fingerprint, quick file hash or matching title before importing it. Nothing
      in the repo called it and it has no replacement; the importer's own
      duplicate handling at ingest is unchanged.
