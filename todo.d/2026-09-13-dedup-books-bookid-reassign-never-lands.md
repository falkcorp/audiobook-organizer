- [ ] **DEDUP-BOOKS-BOOKID-UPSERT** `ddMergeDuplicateBook`
      (`internal/maintenance/jobs/dedup_books.go:346`) moves a duplicate's files to
      the keeper by setting `f.BookID = keeper.ID` and calling `UpsertBookFile`.
      Upsert has always overwritten `BookID` with the stored row's value, so the
      reassignment never lands and the files stay on the duplicate book. Move it
      to `MoveBookFilesToBook`, then add a Pebble-backed test that the keeper owns
      the files afterwards. First measure how many prod rows a past run left
      behind. Found while fixing the rescan field wipe
      (`fix/bookfile-rescan-preserve-fields`).
