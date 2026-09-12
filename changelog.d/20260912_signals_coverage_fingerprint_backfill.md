### Added

- `GET /api/v1/signals/coverage` (settings-manage permission): per-signal coverage of every book_file row (content hash, original hash, per-file duration, raw tags, fingerprint-duration proxy, fingerprint failure tombstones), each split by the stored `missing` flag, plus primary-book and book-embedding counts. The default reads memdb row pointers across a NumCPU worker pool; `?deep=true` scans Pebble for exact raw-fingerprint, Seg0–6, segment-only and failure-reason counts. It never stats a file and refuses (503) instead of silently falling back to a Pebble scan while memdb is warming up.
- `scheduled.acoustid_backfill` TaskScheduler task (`SCHEDULED_ACOUSTID_BACKFILL_ENABLED`, interval 1440 min), **disabled by default**, so the fingerprint backfill can run on a schedule.
- `fingerprint_length_sec` / `FP_LENGTH_SEC`: seconds of audio fpcalc analyses per file. Default 120, which is fpcalc's own default and what every stored fingerprint was made with; negative means the whole file.

### Fixed

- `acoustid.backfill` skipped legacy segment-only files forever: eligibility counted `acoustid_seg0` as "already fingerprinted", so those rows never got a raw print without `force=true`. A Seg0-only row is now eligible whenever fpcalc is available. Ineligible outcomes are now counted with a reason breakdown instead of vanishing from the summary.
- `acoustid.backfill` no longer loads the whole book table up front (the ~862 MB load phase behind the startup gate); it pages 500 books at a time and checkpoints a cursor after each drained page. Older checkpoint formats still resume.
- `acoustid.backfill` fingerprints the files of a book in parallel under one run-wide semaphore sized by `FP_PARALLEL_WORKERS`, so a many-chapter book no longer runs at one fpcalc.
- Removed the op's cron `Schedule` string, which nothing evaluated; its only live effect (EnqueueOp de-duplication by def id) is kept via `DedupeQueuedRuns`.
- `internal/fingerprint` comments claimed fpcalc decodes the whole file; it analyses the first 120 s. The comments now say so, and `-length` is passed explicitly.
