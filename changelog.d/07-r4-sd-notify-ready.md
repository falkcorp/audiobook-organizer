### Added

- The server speaks the systemd notify protocol: it sends `READY=1` once its fatal startup steps have passed and its HTTP port is bound, a fixed `STATUS=` line, and `STOPPING=1` when shutdown begins. It is a no-op without `NOTIFY_SOCKET`, so the binary behaves as before under a `Type=simple` unit.
- Long startup steps (opening the store, each migration applied, opening the search index) send `EXTEND_TIMEOUT_USEC` as they start, so a long migration run under `Type=notify` is not killed at `TimeoutStartSec`. The notify code lives in a new leaf package, `internal/sdnotify`.
- `audiobook-organizer --print-capabilities` prints `sd_notify` for a binary that sends `READY=1`. An older build fails with "unknown flag".
- `scripts/deploy-match-unit-type.sh HOST BINARY` runs before every restart in `make rollback` and in the template `deploy`. A binary without `sd_notify` (for example an older `.prev`) gets a `Type=simple` drop-in (`zz-rollback-type-simple.conf`) instead of being killed at the start timeout. A notify-capable binary has that drop-in removed.
- `scripts/deploy-wait-ready.sh BASE_URL [TIMEOUT]` polls `/health` every 5 s until it reports `"ready":true`, and fails at once on a body with no `ready` field.

### Changed

- `deploy/audiobook-organizer.service` now uses `Type=notify`, `NotifyAccess=main` and `TimeoutStartSec=300`. The owner installs the unit: copy it to `/etc/systemd/system/`, run `systemctl daemon-reload` and restart, and only after a binary with readiness notification is deployed.
- The main HTTP(S) listener is bound synchronously, and the TLS key pair is loaded before the bind, so a port in use or an unreadable key pair fails startup with an error instead of exiting the process from a goroutine later. The port-80 redirect listener is also bound up front, its failure is still non-fatal, and it is now shut down with the main server.
- `make deploy`, `make deploy-debug` (from `Makefile.local.example`) and `make rollback` wait for `/health` `"ready":true` (up to 300 s) instead of sleeping 3 seconds. `make rollback` needs `DEPLOY_URL`. When it is unset, the URL comes from `API_URL` (minus `/api/v1`) or from `DEPLOY_HOST`.
