### Fixed

- **The scanner no longer links the chapter files of one book as "versions" of
  each other, and never leaves a version group with no primary.** The import
  path's smart-dedup step grouped any two records that shared a lowercased title
  and a parent directory. That predicate is chapter-invariant: the per-chapter
  records of one audiobook sit in one folder under one album title, so 27 mp3
  chapters were linked as 27 editions of each other. It then wrote
  `is_primary_version = (format == m4b)`, so in an all-mp3 folder **every** member
  was written an explicit `false` and the group elected nobody. Because the web
  library list and the v2 `bulk_metadata_fetch` filter both apply
  `is_primary_version=true`, those books disappear from the library and are
  excluded from enrichment. Measured on 2026-09-19: 2,586 zero-primary groups
  holding 3,988 books, with the newest row written four days earlier — a live
  writer, not a historical mess.

  Grouping now requires positive evidence of two copies of one *whole* book.
  Neither record may read as a positional part — the check reuses the chapter-split
  detector's own `ParseSequenceMarker` / `ParseFilenameSequence` markers and its
  `" (N)"` copy-suffix pattern rather than a second title parser — known durations
  must not disagree by more than 5%, and then either the formats differ (the
  feature's stated purpose, an .m4b and an .mp3 of one book) or one file carries
  the `" (N)"` copy suffix over the other's stem. The positional-part gate applies
  to the mixed-format case too, so a pair whose file names end in a track number
  — what `config.DefaultFileNamingPattern` (`{title} - {track:02d}`) produces for
  every organized single-file book — reads as parts and is left ungrouped. Under
  that pattern the mixed-format link is effectively off; this deployment's
  configured pattern is not the default and is unaffected. Same-format records with no copy
  suffix no longer group on similar durations alone: two adjacent chapters are the
  likeliest pair in a library to have similar durations.

  Primacy is now elected, explicitly, for the whole group: an .m4b first
  (unchanged), then the longest known duration, the largest known file size, the
  existing row over the row being created, and lowest ID last. A group that
  already has a primary never gets a second one, soft-deleted and merged-away
  siblings can no longer be crowned, and if the elected sibling's write does not
  land the new row takes primacy rather than leaving the group empty-handed. A
  record whose every sibling link fails is left with no group at all instead of
  becoming an orphan group of one, and a row a content-hash branch has already
  grouped is left alone rather than being re-grouped by title.

  One downstream interaction to watch: rows this now leaves ungrouped become
  candidates for `reconcile.AssignOrphanVGs`, which mints a group of one AND
  stamps `library_state = "organized"` — so an imported row it picks up becomes
  ABS-visible before anything organized it. That is pre-existing behaviour of
  that op, not new here, but this change feeds it more rows.
