### Fixed

#### Updater: check-only unattended runs, exit 75, the kept `.prev` slot, semver comparison

Automatic updates now only check for a new release and notify you; apply
updates from Settings. A locally built binary is not compared with releases.

- A restart after an update exits with status 75, so the committed unit's
  `Restart=on-failure` brings the service back up (it exited 0 before and the
  service stayed stopped).
- The previous binary is kept as `<exe>.prev`, the slot `make rollback` uses,
  instead of being renamed to `<exe>.old` and deleted.
- Versions compare with `golang.org/x/mod/semver`. A release that is not newer
  than the running version is never offered and never applied (any different
  tag, including an older one, counted as an update before).
- A running version with a `git describe` suffix (`-<n>-g<hex>` or `-dirty`)
  is a local build: the check reports "local build, not compared" and offers
  nothing, and `POST /api/v1/update/apply` refuses it unless the body names the
  version (`{"version": "<tag>"}`).
- Neither the scheduled updater nor step 1 of the maintenance window applies an
  update any more; they record the check and log it. While
  `auto_update.enabled` is on, `GET /api/v1/update/status` reports
  `auto_apply_paused`, `GET /api/v1/system/status` gains a `degraded` list
  carrying `auto_update_paused` (never shown on `/health`), and Settings shows a
  banner.
- An apply downloads the release that was checked or named, by its tag, rather
  than whatever is latest at that moment.
