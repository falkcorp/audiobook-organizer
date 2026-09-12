### Fixed

- `acoustid.backfill` ended its walk on any page shorter than 500 books and reported the run complete. Only an empty page ends it now. A resume whose cursor is the last book finishes instead of re-listing the library from the top; a resume whose first page is empty and whose cursor book no longer exists still restarts from the beginning.
- `GetAllBooksFullFrom` (memdb path, the production default) looked the cursor up by exact match and returned nothing when the cursor book had been merged or deleted, silently ending every paged walk that hit one. It now seeks to the first ID strictly greater than the cursor by binary search, matching the Pebble branch's key seek, and fills the page past rows that vanish between listing and loading, so a short page means the end of the table. A point-read error is now returned instead of silently dropping the book.
- `GET /api/v1/signals/coverage?deep=true` runs one deep scan per store at a time; a concurrent deep request gets 409. The deep scan hands rows to its decoders in batches of 128 through a one-slot channel, so at most about (workers + 2) × 128 copied rows are in flight (previously up to (3 × workers + 1) × 512).
- The coverage fast path's 503 no longer claims "warmup has not completed" in every case. It says which is true: memdb is disabled on the store (`UseMemDB=false`), or it is not yet published (warmup still running, or it failed and reads fell back to Pebble).
- `fingerprint_length_sec` / `FP_LENGTH_SEC` is clamped to 600 with a warning; negative values still fall back to 120 and whole-file mode remains unreachable from config.

### Changed

- `acoustid.fingerprint-rescan` with `scope=missing` shares the backfill's eligibility check, so it now also reaches Seg0-only rows. On such a row the legacy ffmpeg Seg0 is replaced by a Seg0 derived from the new 120 s fpcalc print, while Seg1–Seg6 are left as they were (only `force=true` clears them), so those rows carry mixed provenance: an fpcalc Seg0 beside ffmpeg Seg1–6. A row that already has a raw print (or a fingerprint duration) is never selected by this check, so neither op replaces an existing raw print unless `force=true` is passed.
