### Fixed

- `make rollback` and `make rollback-restore` now put back the previous release's systemd unit and `local.conf` drop-in together with its binary. Before this, a rollback started the old build under the new environment, which after an env-name change fails open on the peer allowlist and drops the Cloudflare Access, ABS JWT and owner-email settings. Each deploy through `install-staged` keeps the unit and drop-in it replaced as `<bin>.prev.env` beside `<bin>.prev` (written atomically, committed only when `.prev` rotates), and a rollback with no kept environment, or one belonging to a different binary, refuses before anything is stopped. `ROLLBACK_ENV=snapshot` says a kept environment fits a `RELEASE=` binary.

### Added

- `make deploy-env-check`, run first by `make deploy`, `make deploy-debug` and `deploy-preflight.sh`: refuses a `Makefile.local` whose deploy recipes bypass `install-staged` (they keep no `.prev`, unit or `local.conf`; `DEPLOY_ALLOW_NO_ROLLBACK=1` overrides), and a `deploy/local.conf` that sets an env name the new binary ignores. The retired names come from `deploy/retired-env-names.txt`, generated from the rename table and pinned to it by a test; the check reports only, until the app wires `RetiredConfigNames`.
