<!-- file: changelog.d/20260730_060600_abs-sync-chapter-persistence.md -->
<!-- version: 1.0.0 -->
<!-- guid: 83e4309d-6551-404b-816b-11b475cf8a92 -->
<!-- last-edited: 2026-07-30 -->

### Added

- **Persisted chapter storage (abs-sync Phase 4).** Added a `database.Chapter` type and
  `PebbleStore.{Get,Save,Delete}ChaptersForBook` methods, storing one ordered chapter list per book
  under a new `chapters:<bookID>` Pebble key. Chapters are deleted when their book is deleted. Pure
  persistence layer — extraction (ffprobe) and scanner wiring land in a follow-up task.
