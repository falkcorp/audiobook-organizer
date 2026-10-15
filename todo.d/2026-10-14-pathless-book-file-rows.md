- [ ] **Pathless `book_file` rows: nothing surfaces or clears them** — a row with
      a `BookID` but an empty or whitespace `FilePath` is skipped by
      `maintenance.mark-missing-files`, skipped by `dedupe_book_file_rows.go`
      (its comment at ~line 362 said `orphan-book-files-cleanup` owns them; that
      was false, and is now corrected), and not an orphan to
      `orphan_book_files.go` (~line 178 tests only `BookID == ""`). No op
      reports them or repairs them. Any such row the retired
      `maintenance.fix-book-file-paths` job set `Missing=true` on (04-P14d)
      stays flagged forever, because the survivor never revisits a pathless row.
      First step: a read-only census of the prod library — total pathless
      `book_file` rows, and how many of them have `Missing=true`. Then decide
      between a review-tab finding with a repair (re-derive the path, or retire
      the row) and having the Broken Files count include them.
