### Removed

#### iTunes: the incremental sync is gone; import is the one iTunes action

Owner decision 2026-10-08: iTunes is imported only when someone clicks
**Import iTunes library**, and nothing runs on its own. Removed:

- `POST /api/v1/itunes/sync`, the `itunes.sync` operation (server op and the
  plugin stub; the ID is retired in the op-ID ledger), `Importer.Sync`,
  `syncLibrary` and `ErrSyncDisabled`.
- The pre-organize iTunes sync (`syncITunesBeforeOrganize`, the
  `sync_itunes_first` organize parameter and the Dashboard's "Sync iTunes
  library first" checkbox).
- The `itunes_sync` scheduled-task binding and the unused `LibraryWatcher`.
- The `itunes.sync_enabled` / `itunes.sync_interval` settings and their
  `ITUNES_SYNC_ENABLED` / `ITUNES_SYNC_INTERVAL` env bindings. Stored values
  and env vars still load without error and are ignored.
- The Settings "Force Sync Options" panel (Sync Now, Force Import, Retry
  Failed Sync) and the conflict dialog, whose `/itunes/resolve-conflicts`
  endpoint never existed on the server.

The `itunes_sync` activity type stays, because historical rows use it.

### Changed

#### iTunes import can be re-run safely and reports linked, added and skipped

Re-importing matches each album before adding anything: a tombstoned iTunes
ID is skipped, an ID already mapped to a book links to it, and otherwise the
one live book at the album's path or holding one of its track PIDs links.
Only an album with no match becomes a new book. A link now refreshes the
book's iTunes play count, rating, bookmark and last-played date (only the
fields the source format carries, so an `.itl` re-import never zeroes a
bookmark). It never moves a file or changes a stored `FilePath` on the book
or any `book_file` row. The iTunes-ID rules (external-ID map, tombstones,
book_file PIDs, PID moves on merge, retire and repoint) are unchanged.

Tombstoned albums, and albums whose match is ambiguous or marked for
deletion, now count as **skipped** (they were uncounted or counted as
failed). The import-status API returns `linked`, and the Settings panel shows
"Linked N, added N, skipped N" when a run finishes. When books are already
linked to iTunes, clicking **Import iTunes library** first shows a warning
that albums iTunes re-created with new IDs, or whose files moved, will be
added as new books; the choices are **Import anyway** and **Cancel**.

The merge iTunes guard's "a library exists but its location is unknown"
refusal now keys on a configured `.itl` path (`itunes.library_write_path`)
instead of the removed `itunes.sync_enabled`, which defaulted to on.
