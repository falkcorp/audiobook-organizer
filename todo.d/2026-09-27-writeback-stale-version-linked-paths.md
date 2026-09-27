- [ ] **WRITEBACK-STALE-PATHS** Tag write-back fails on stale `book_file`
      paths of version-linked books. In op `01M3HX18SKGSV08MC7R6XFKR3D`, 10
      books were applied to the database but their tag write-back failed
      (e.g. Wyrd Sisters, Starred Tower). The write-back used `book_file`
      rows whose paths no longer exist, because the rows belong to a
      version-linked twin whose files moved. Find where the write-back
      resolves file paths for a version-group member, make it use the book's
      present active files, and re-run write-back for those 10 books. Done
      means a test with a version-linked book whose sibling's rows are stale.
