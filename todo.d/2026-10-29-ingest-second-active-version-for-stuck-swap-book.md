- [ ] **TODO-INGEST-ACTIVE-WHILE-SWAP-STUCK** Stop ingest from creating a second active version
      for a book whose version swap did not finish. A book with a version in `swapping_out` /
      `swapping_in` has no active version, so `versions.CreateIngestVersion` (`internal/versions/ingest.go`, the
      `GetActiveVersionForBook` check that picks `active` over `alt` for a new version) makes
      the new download the book's active version, and the later
      `versions.ResumeVersionSwap` then refuses (409 `SWAP_RESUME_REFUSED`: another version is
      active) and leaves the book needing a hand repair. Needed: the ingest path that decides
      "this book has no active version" must treat a `swapping_*` version as an unfinished swap
      (use `ListSwappingBookVersions`, or check the book's versions) and add the download as
      an `alt` version, or hold it, until the swap is resumed. Add a test that ingests into a
      book left stuck by a `RunVersionSwap` that returned `ErrSwapIncomplete`. Found while
      doing TODO-SWAP-STUCK-RECOVERY; the resume itself only refuses, it does not pick a winner.
