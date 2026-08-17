- [ ] **Decide what to do with the books whose EVERY `book_file` row is dead.** The
      general repair is decided and built (`maintenance.missing-file-repair`, option
      "delete only where the book keeps a surviving file"), but it deliberately
      skips books with no surviving file — 5 of 120 in the sample. Deleting their
      rows would leave the book with nothing at all. Options: locate the audio by
      filename/size/hash and re-point the row, mark the book as missing rather than
      deleting, or leave it. The repair op names these books in its report, so run
      the audit + a dry run first and decide against the real list.

- [ ] **Answer why the organizer recorded destination rows it never populated.**
      Every dead path is under the organizer's own destination tree and none under
      the iTunes tree, which points at the library-wide move in #2479. The repair
      cleans up the symptom; this is the cause, and without it the rows come back.

- [ ] **Register `HEAD` for the audio/file routes.** The server registers no `HEAD` handler
      anywhere, so `HEAD /api/items/:id/file/:ino/download` 404s on a file that exists. Upstream
      Audiobookshelf runs on Express, which auto-answers `HEAD` for a `GET` route; gin does not.
      Not currently causing failures — the production journal shows real clients only send `GET` —
      but any client that preflights with `HEAD` would see "file not found".
