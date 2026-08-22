### Changed

- **iTunes import and sync no longer create legacy v1 operation rows.** Both ops
  now run entirely on operations v2: the id returned by `POST /itunes/import` and
  `POST /itunes/sync` names a v2 run, and the import-status endpoints read that
  row. The response shape is unchanged, but `status` now reports the full v2
  vocabulary (`queued`, `running`, `completed`, `failed`, `canceled`,
  `interrupted_*`) instead of the narrower v1 set it was mirrored into.

### Fixed

- **The iTunes import panel could spin forever on an import that had already
  finished.** Its poller stopped only on `completed` or `failed`, so cancelling
  an import — or having the server restart mid-import — left the progress bar
  animating and the 2-second poll re-arming indefinitely. Terminal-status
  detection is now shared and covers cancellation and every `interrupted_*`
  variant.
- **The iTunes panel no longer detects an import already running when you open
  it.** It matched an operation type (`itunes_import`) that the operations-v2
  store never emits — it exposes the def id's tail segment (`import`) — so the
  check silently never fired and a running import looked idle until you
  refreshed. It now matches on `def_id`.
