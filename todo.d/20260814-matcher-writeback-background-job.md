- [ ] **Metadata matcher: multi-file write-to-files must dispatch as a
      background operation.** Owner request (2026-08-14): with "write to
      files" enabled and more than 1 file affected, the apply currently
      blocks the UI until every file is rewritten — at the measured
      ~35 s/file for a full tag rewrite that is minutes-to-forever from the
      user's chair. Route the >1-file case through the operations system
      (`maintenance.bulk-write-back` already exists, takes explicit
      book_ids, and shows in the ops UI) and return immediately with the
      op id; single-file applies can stay synchronous. Note bulk-write-back
      is serial ~35 s/book — the E08 prerequisites fragment
      (diff-skip + in-op parallelism) applies here too.
