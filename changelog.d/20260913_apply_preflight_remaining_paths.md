### Fixed

- `POST /api/v1/metadata/batch-apply-candidates` now runs the rename preflight
  (`metafetch.RenamePreflight`) before writing each book, whenever the file-IO
  pool will queue the rename. A book whose post-apply rename is known to fail is
  refused before any write (no metadata, no "applied" op-result row, no file
  job) and listed under `blocked` with reason `file_work_would_fail`, so it can
  no longer end up with new metadata and old file names (the 2026-09-13
  failure). The metadata upgrade op (`metabatch.RunUpgrade`) and the
  transcription auto-apply (`ApplyTranscriptionCandidate`) were checked and
  queue no file work after their apply, so no rename follows them; both now say
  so in a comment, and the upgrade op's comment claiming it queued a rename is
  corrected.
