## Scan cache is keyed per-book but the skip decision is per-file

Multi-file books are re-read and re-hashed on **every** scan. Normalizing a book's
`FilePath` to its directory (which is correct for the book) destroys the cache key for
every file inside it. Two independent causes, both measured 2026-08-24 — fixing either
alone changes nothing:

1. **Key grain.** `GetScanCacheMap` (`internal/database/pebble_store_scancache.go:44`)
   keys on `book.FilePath` — the directory. The walk emits, and `classifySkipFile`
   (`internal/scanner/scanner.go:539`) looks up, the **segment file** path. Grouping makes
   zero store calls, so it cannot know the row moved. Every lookup misses.
2. **Value grain.** `writeBackScanCache` is handed the **directory** to stat, so the stored
   size is the directory inode's (128 bytes observed) rather than the segment's. Even with
   keys aligned, the `entry.Size != size` comparison at `scanner.go:546` fails.

Measured second-scan verdict: `skippedUnchanged=0 cacheMiss=1`.

Fix direction: key the scan cache per **book_file** rather than per book. Relates to the
per-file transcription/backfill grain work. Needs a design decision before implementation —
do not bolt a second cache onto the book row.

- [ ] Decide per-file scan-cache keying and write it up before coding
- [ ] Confirm whether the directory-rooted book branch (`scanner.go:1229`, never calls
      `writeBackScanCache` at all) folds into the same fix
- [ ] Open question, not yet measured: does the real `saveBookToDatabase` create a
      **duplicate row** for an already-normalized multi-file book on rescan? A simplified
      stub did; production's segment-hash dedup may re-link instead. Measure before assuming.
