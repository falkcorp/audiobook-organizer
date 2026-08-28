<!-- file: changelog.d/20260828_120000_organizer_target_suffixes.md -->
<!-- version: 1.0.0 -->
<!-- guid: a8a1d774-f882-43c5-8c08-4880d9e348cd -->
<!-- last-edited: 2026-08-28 -->

### Fixed

#### Organizer preserves files when generated destination names collide

When two different books expand to the same organized filename, the first
destination remains untouched and the next file is written as
`filename_copy1.ext`, then `filename_copy2.ext` as needed. This preserves both
audio files and keeps their extensions unchanged while retaining the existing
exclusive-create transfer safeguards against overwrites.
