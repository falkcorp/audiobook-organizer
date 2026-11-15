### Changed

- The 14 maintenance definitions of T6 group 4 (dedup batch and triage, file-row dedup, duration backfill, WAV clip extraction, per-file and per-book intro transcription, intro migration, file provenance capture and export, filepath collision report, filesystem regroup, ident index rebuild, integrity check) each gained a dark native `books.<id>` task kind beside the unchanged v2 definition. Nothing registers the kinds yet, so no behaviour changes in production.
- The native `books.maintenance.transcribe-file-intros` kind stops starting batches within five minutes of its deadline, saves a cursor and continues in place; the native duration-backfill book grain runs its workers under a declared `Fixed(workers)` loop. Both are dark until the kinds are registered.
