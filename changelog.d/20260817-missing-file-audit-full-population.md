### Fixed

- Blocked the approved `maintenance.missing-file-repair` apply before it ran.
  A full-library `missing-file-audit` (532,296 rows) plus an on-disk
  discriminating test showed that a large share of "missing" `book_file` rows
  are database residue of the 2026-03-03→2026-08-15 `segment_title_format`
  slash bug, and their audio files are still on disk under the current
  `{title} - {track:02d}` name. 24 of 24 tested delete candidates (768.5 MB)
  pointed at files that exist. Those rows need repointing, not deletion.
