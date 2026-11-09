### Added

- The first start after this upgrade keeps a full copy of the database,
  including saved passwords and sign-in tokens, beside it for 7 days;
  `audiobook-organizer storage prune-checkpoints` deletes it sooner.
- A checkpoint sweeper now runs once every start, right after the server is
  ready, and then every hour. It deletes each database copy 7 days after it
  stops being needed (a completed upgrade's copy 7 days after the upgrade; a
  failed upgrade's copy is kept while it is the only way back, then 7 days
  more), removes half-written copies, and deletes leftover dry-run and
  rehearsal copies 7 days after they were last changed. The 7 days are fixed;
  no setting makes them longer. It also checks that every copy is readable
  only by the service account and logs an error naming any file or folder
  that is not; it never changes a permission itself.
- `audiobook-organizer storage prune-checkpoints` deletes completed and
  released copies now, and with `--include-open --confirm` also the copy a
  failed upgrade is still using as its way back. It works whether the server
  is running or stopped, and refuses while an offline restore is in progress.

### Changed

- `GET /api/v1/system/status` (signed in, settings permission) now lists
  every degraded token in `degraded`, including the ones `/health` shows,
  and adds `storage_checkpoint_retained` (a database copy exists, with when
  it will be deleted), `storage_checkpoint_open` (a copy still in use, or a
  leftover dry-run copy, with its age) and `storage_checkpoint_mode` (a copy
  readable by other accounts, naming it). The details are in a new
  `degraded_details` list. `/health` still shows only `memdb_warming`,
  failed optional startup steps such as `search_index`, and
  `basic_gate_no_password`.
