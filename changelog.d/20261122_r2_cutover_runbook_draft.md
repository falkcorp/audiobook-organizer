### Added

- A draft R2 cutover runbook, `docs/system/r2-cutover-runbook.md`, for the owner. It has a path for an install already migrated flip by flip (the production host) and one for an install coming from the pre-freeze release. Each step has the exact commands, the check that shows it worked, and its own undo. It records what the F3-07 deploy of 2026-11-22 already did (the env rename, `host.conf`, the rollback helpers) and what it showed about the systemd start timeout, and lists the gaps still open. Linked from `docs/system/runbooks.md`.
