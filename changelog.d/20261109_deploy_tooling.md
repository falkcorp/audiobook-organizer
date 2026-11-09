### Changed

- Deploy waits on the systemd unit and reports exit 78 by name; the unit no longer restarts on exit 78 and stops within 60 s; `make rollback-restore` for releases that change the store.
- `make rollback` refuses when the store is at a level the previous binary cannot read (`<db>.storage-levels` against `.prev --print-storage-levels`), when a checkpoint pointer exists without a levels file, and while a restore or reset swap is in flight.
- Every privileged rollback step runs through root-owned helpers in `deploy/rollback-helpers/` that validate their own arguments and read their paths from `/etc/audiobook-organizer/rollback.conf`; the sudoers drop-in grants only those helpers and fixed commands, with no wildcards and no `tee`. The documented drop-in is rendered by `scripts/storage_format_guard.py sudoers`. `make rollback`, `make rollback-restore` and `scripts/deploy-match-unit-type.sh` need the helpers installed (owner action).
- `make deploy`, `make deploy-debug` and both rollbacks wait `DEPLOY_READY_TIMEOUT` seconds (default 300) after the unit leaves `activating`, and refuse to report ready until the unit's `InvocationID` differs from the one read before the restart, so a restart that never ran no longer passes. `scripts/deploy-wait-ready.sh` without an ssh host waits at least `DEPLOY_READY_URL_TIMEOUT` seconds (default 1800).
- Rollbacks install `.prev` atomically, match the unit type before stopping the service, run the restore as the service user in a transient unit that survives an ssh drop, and print the host's state and the commands that finish or undo the rollback when a step fails. `ROLLBACK_ARCHIVE` is a file name in the helpers' `archive_dir`.
- `storage restore-offline --into <dir>` writes the copy's own `<dir>.storage-levels` beside it.

### Fixed

- `scripts/deploy-wait-ready.sh` reads `ready` inside `/health`'s `{"data": ...}` envelope; it required a top-level `ready` and exited 2 against every real server.
