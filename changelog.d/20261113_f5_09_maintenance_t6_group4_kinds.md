### Changed

- The 14 maintenance definitions of T6 group 4 (dedup batch and triage, file-row dedup, duration backfill, WAV clip extraction, per-file and per-book intro transcription, intro migration, file provenance capture and export, filepath collision report, filesystem regroup, ident index rebuild, integrity check) each gained a dark native `books.<id>` task kind beside the unchanged v2 definition. Nothing registers the kinds yet, so no behaviour changes in production.
