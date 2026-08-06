<!-- file: changelog.d/20260805_215000_todo_chapters_playlists_deluge.md -->
<!-- version: 1.0.0 -->
<!-- guid: c6b0842f-3d19-4e57-a870-51ec93b4f2d6 -->
<!-- last-edited: 2026-08-05 -->

### Added

- **Planning: six owner-requested workstreams captured as `todo.d/` fragments.**
  Documentation only — no code or runtime change. Covers chapter delivery to
  clients, chapter backfill from duplicate copies, full playlist support,
  reading/review status sync, and two uses of Deluge as an external identity
  source. Each fragment records the constraint that makes it non-trivial rather
  than just the ask: chapter backfill must be gated on a near-exact acoustic
  match (offsets borrowed from a different edition read as correct and silently
  mis-seek), and Deluge torrent membership is an upper bound on one book rather
  than proof of one book, so it carries the same over-merge risk as the folder
  heuristic.
