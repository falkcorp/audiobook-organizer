- [ ] **Decide the repair for `book_file` rows whose bytes are gone.** `maintenance.missing-file-audit`
      now measures them (41.8% of rows in a 120-book sample; every one under the organizer's own
      destination tree, none under the iTunes tree). Two candidate repairs, which differ in kind:
      delete the phantom row, or re-point it at the surviving file. Deleting is **not** uniformly
      safe — books whose every row is missing would be left with no files at all, so those need a
      separate decision. Run the audit library-wide first, then choose. Also worth answering: why
      the organizer recorded destination rows it never populated (suspect the library-wide move
      in #2479).
- [ ] **Register `HEAD` for the audio/file routes.** The server registers no `HEAD` handler
      anywhere, so `HEAD /api/items/:id/file/:ino/download` 404s on a file that exists. Upstream
      Audiobookshelf runs on Express, which auto-answers `HEAD` for a `GET` route; gin does not.
      Not currently causing failures — the production journal shows real clients only send `GET` —
      but any client that preflights with `HEAD` would see "file not found".
- [ ] **Differentiate the five `"file not found"` returns** in `internal/server/handlers/abs/stream.go`
      (lines 53/78/95/145/159). They mean ino-absent, no syncfile for this book, no `book_file` for
      the syncfile's `CurrentFileID`, `filepath.Abs` failed, and bytes-missing — and none of them
      logs, so every one presents identically and the next report is undiagnosable.
