### Removed

#### The SQLite activity backend

The SQLite activity backend is removed (owner decision D11). Pebble, inside the main database, is now the only activity store, which is what production has run on. The last build that can still use SQLite is tagged `activity-sqlite-last`.

- A config file or an `ACTIVITY_BACKEND` environment variable that still says `activity_backend: sqlite` now stops startup with a clear message telling you to remove the setting or set it to `pebble`. Any other value that is not `pebble` or empty is refused the same way. A settings update (`PUT /api/v1/config`) that tries to select `sqlite` is rejected with 400. A stored config saved while SQLite was selectable still loads: the server ignores the stored value and uses `pebble`, as it already did.
- The old `activity.sqlite` file is left where it is. Nothing reads it or deletes it. Delete it by hand when you no longer need it.
- The `activity_db_path` and `activity_db_move_on_change` settings, the read-only `activity_db_resolved_path` field in `GET /config`, and the Activity Database section of Settings, Paths are gone. A stored config or legacy settings row that still has either setting loads with a warning. A settings update that carries either one is rejected with 400 and a message saying it was removed.
- Three SQLite-only maintenance operations are removed: `maintenance.optimize-activity-db` (including its nightly schedule), `maintenance.vacuum-activity-db` and `maintenance.activity-reclaim`. The Pebble-to-SQLite migration that ran at startup is removed with them.
- The 8 KiB cap on activity summaries used to apply only when SQLite stored them. Pebble now applies it on every write, so a runaway multi-megabyte summary can no longer reach storage.
- `modernc.org/sqlite` and its dependencies are no longer part of the build.
