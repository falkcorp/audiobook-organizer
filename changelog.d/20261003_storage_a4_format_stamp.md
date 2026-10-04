### Added

#### Storage format stamp checked at every open

The main Pebble store now carries a `storage_format` stamp (preference
`storage_format`, starting at `1`, today's format) mirrored in a
`<db>.storage-format` sidecar file. Every open refuses a store whose stamp or
sidecar is newer than the build supports, refuses an older store with "start
serve to migrate", and refuses a store carrying the `storage_migration` marker
(restore the checkpoint it names, or re-run the cut-over mode). A refused open
writes nothing and does not raise the Pebble on-disk format: the open phase
opens with no format version and the init phase ratchets to the pinned one.
The binary prints its supported format with `--print-storage-format`. No data
changes.

#### Reserved preference keys and a recovery command

`PUT`/`DELETE /api/v1/preferences/:key` now return 400 for `storage_format`,
`storage_migration`, `db_version` and `migration_<n>`, and the store refuses
generic writes to the first two (`ErrReservedPreferenceKey`). A store that
already holds a bad value in one of them can be repaired with the new
`diagnostics reserved-prefs` command (`--delete KEY`, `--set KEY=N`), which
works on a store the guard refuses, asks for confirmation (or `--yes`), and
prints and logs every key it changed, including the preference counter and the
sidecar, even when a later step fails. It caps `--set db_version` at the
highest registered migration, refuses to delete the stamp once a build supports
a format above 1, and never creates a store at a mistyped path.

#### Pinned Pebble on-disk format

The main store is ratcheted to the shared `database.PebbleFormatMajorVersion`
(after its storage-format checks pass) instead of opening at
`pebble.FormatNewest`, so a pebble dependency bump can no longer silently
ratchet the on-disk format. `diagnostics query --raw` and `diagnostics
reserved-prefs` pass no format version at all, so an inspection or repair never
raises the format.
On pebble v2.1.7 the pinned value equals `FormatNewest`, so nothing changes on
disk.

#### `make rollback` refuses past a storage format change

`make rollback` now swaps in `.prev` only when the store's format is not above
the format `.prev` supports (`scripts/storage_format_guard.py`); otherwise it
refuses and prints the restore-from-checkpoint steps
(`docs/system/runbooks.md#storage-format-restore`). It needs the new
**required** `DEPLOY_DB` variable (the main Pebble store path on the deploy
host) in `Makefile.local`, and passwordless sudo for the commands listed in
`docs/system/deploy-and-gpu-ops.md`; a refusal quotes the ssh stderr that
caused it. `ROLLBACK_IGNORE_FORMAT=1` overrides only an unreadable sidecar,
and still refuses unless the current binary answers `--print-storage-format`
with a format no higher than `.prev`'s (or is a build that predates the flag).
