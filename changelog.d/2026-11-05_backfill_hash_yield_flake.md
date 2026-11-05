### Fixed

- The backfill-file-hashes yield-margin test no longer races a real 400ms deadline against real sleeps: the job's yield decision reads an injectable clock, so a loaded CI agent can no longer make the deadline arrive before the first chunk finishes.
