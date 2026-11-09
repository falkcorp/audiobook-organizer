### Changed

- A restore now takes effect at the next restart, which the service performs itself; a container started without a restart policy must be started again.
- In this release a factory reset or a database reset runs only from the command line, with the service stopped (`audiobook-organizer storage reset`); the reset button in Settings returns with <R2 release>. Undoing a factory reset restores the database but not the library files or Open Library data it deleted. A reset now leaves `config.yaml` beside the database unchanged; delete it by hand for a full reset.
- Restoring a backup from Settings stages it and restarts the service; the page reloads once the server is back. With "Verify backup" on, a backup without a checksum file is refused instead of restored unchecked, and a refused restore now says why (another restore or reset already waiting, a checksum mismatch, a backup that is no longer listed) instead of calling the file corrupt.
- `storage restore-offline` (`--checkpoint`, `--archive`, `--into`, `--accept-loss`) restores with the service stopped: in place through the same journaled swap a restart uses, or into an empty directory.
- Every command-line tool that opens the database (`seed`, `diagnostics`, `dedup-bench`, `transcode-skip` and the others) now opens it the way the service does, and refuses while the service holds it, while a restore or reset is waiting or half-applied, or before the service has created it. Stop the service first.
- The updater refuses to apply an update while a restore or reset is waiting or in progress.

### Removed

- The `POST /api/v1/system/reset` and `POST /api/v1/system/factory-reset` routes, and the reset control in Settings, until the core reset route lands.
- The `target_path` field of `POST /api/v1/backup/restore` (answered 400): restore into another directory with `audiobook-organizer storage restore-offline --archive <file> --into <dir>` instead.
- The `pebble-inject-skip` tool. `audiobook-organizer transcode-skip <file>...` writes the same transcode-skip settings through the guarded open, with the service stopped.
