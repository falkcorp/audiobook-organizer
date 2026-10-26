- [ ] **TODO-BF-CAS-RETIRE-BACKOFF-UNDER-LOCK** `retireBookFilesAttempt`, `unretireBookFilesAttempt`
      and `rehomeRetiredVersionAttempt` (`internal/database/retired_book_files.go`) hold the
      version stripe (`lockBookVersion`) across `restageBookFileBatch`, whose backoff sleeps
      between whole-batch re-stage rounds (up to 9 sleeps of 5-100 ms, under 1 s in all) therefore
      happen with the stripe held, and batch upserts that touch that version wait for them.
      Added latency under contention, not a deadlock. Fix: release the stripe around the sleep
      and re-read the version's rows after re-taking it (the compare already makes that safe),
      at the cost of reshaping the three attempt loops around a shared helper.
- [ ] **TODO-BF-CAS-UPSERT-RETRY-MULTIPLIER** `UpsertBookFile` (`internal/database/pebble_store_bookfiles.go`)
      runs up to `maxStageRetries` match rounds, each calling `updateBookFile`, which has its own
      `maxStageRetries` compare-and-swap rounds: up to 25 attempts under sustained churn on one
      row, each match round taking and releasing the file stripe. Bounded, but undocumented.
      Fix: a comment at the match loop naming the multiplication, or one shared budget passed
      down to `updateBookFile` (changes its signature and every caller).
- [ ] **TODO-BF-ACOUSTID-SEG-KEYS-NOT-PERSISTED** `UpdateBookFile` writes `book_file_acoustid:<seg>`
      index keys for `AcoustIDSeg*` values that `marshalBookFileDropSegs` does not persist in the
      row. A later `SweepBookFileSegDrop` then sees no segment needle in the stored row and skips
      it, so those index keys outlive any row that names them. Pre-dates PR #4180 (which replaced #4177). Fix: stop
      writing the index keys for values the row does not store (or persist the values), and add a
      one-off repair that deletes `book_file_acoustid:` keys whose row has no matching segment.
