### Changed

- Deploy waits on the systemd unit and reports exit 78 by name; the unit no longer restarts on exit 78 and stops within 60 s; `make rollback-restore` for releases that change the store.
- `make rollback` refuses when the store is at a level the previous binary cannot read (`<db>.storage-levels` against `.prev --print-storage-levels`), and while a restore or reset swap is in flight, and names the command it could not run when a sudo rule is missing. The sudoers drop-in in `docs/system/deploy-and-gpu-ops.md` gains the lines both rollbacks need.
- `make deploy`, `make deploy-debug` and `make rollback` wait `DEPLOY_READY_TIMEOUT` seconds (default 300) after the unit leaves `activating`, so a long first-boot migration no longer fails the deploy. `scripts/deploy-wait-ready.sh` without an ssh host waits at least `DEPLOY_READY_URL_TIMEOUT` seconds (default 1800).
- `storage restore-offline --into <dir>` writes the copy's own `<dir>.storage-levels` beside it.
