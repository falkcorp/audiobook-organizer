<!-- file: docs/system/deploy-and-gpu-ops.md -->
<!-- version: 1.7.0 -->
<!-- guid: d5e7f9a1-b3c5-4d7e-9f1a-3b5c7d9e1f3a -->
<!-- last-edited: 2026-11-09 -->

# Deploy Rollback & Windows GPU Keepalive

This document closes the operational gaps flagged in
[`docs/consultancy/06-process-and-security.md`](../consultancy/06-process-and-security.md)
as **OPS-1** (single-machine deploy recipe, no rollback), **OPS-2** (Windows
GPU box kept alive by a scheduled task whose setup scripts existed only in a
scratchpad), and **OPS-6** (operational knowledge landing outside git). See
also [`docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md`](../archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md)
for the local-backend cutover this Windows box supports.

## 1. Instantiating the local, gitignored config from the committed templates

Both `Makefile.local` and `deploy/local.conf` are gitignored (they hold
machine-local paths, TLS cert locations, and the real `DEPLOY_HOST`). Two
sanitized templates are committed instead:

```bash
cp Makefile.local.example Makefile.local
$EDITOR Makefile.local          # set DEPLOY_HOST, DEPLOY_BIN, fix the scp source path

cp deploy/local.conf.example deploy/local.conf
$EDITOR deploy/local.conf       # set real TLS cert/key paths, DB path, WHISPER_REMOTE_URL
```

`deploy/local.conf` is deployed as a systemd drop-in at
`/etc/systemd/system/audiobook-organizer.service.d/local.conf` (see
[`runbooks.md`](runbooks.md#systemd-service) for the existing deploy runbook);
`Makefile.local` is picked up automatically by the committed `Makefile` via
`-include Makefile.local`.

## 2. Rollback flow

`Makefile.local.example`'s `deploy:` recipe now preserves the previously
deployed binary before overwriting it:

```
ssh $(DEPLOY_HOST) 'sudo cp $(DEPLOY_BIN) $(DEPLOY_BIN).prev 2>/dev/null; \
  sudo mv /home/USER/audiobook-organizer $(DEPLOY_BIN) && ...'
```

Once your real `Makefile.local`'s `deploy` target includes this same
`.prev`-preserving line, the flow is:

1. `make deploy` — builds, ships, and (via the line above) copies the
   currently-running binary to `$(DEPLOY_BIN).prev` on the server before
   installing the new one and restarting the service.
2. If the new deploy is bad, run the committed rollback target:
   ```bash
   make rollback DEPLOY_HOST=192.0.2.10 DEPLOY_BIN=/usr/local/bin/audiobook-organizer DEPLOY_DB=/path/to/audiobooks.pebble DEPLOY_URL=https://192.0.2.10:8484
   ```
   (`DEPLOY_HOST`/`DEPLOY_BIN`/`DEPLOY_DB`/`DEPLOY_URL` are normally already
   set in your `Makefile.local`, so you can usually just run `make rollback`.
   `DEPLOY_DB` is the main Pebble store path on the server, the `--db` /
   `DATABASE_PATH` value; `DEPLOY_URL` is the server's base URL, which the
   readiness wait polls. `rollback` refuses to run without either.)
3. `rollback` first checks that `$(DEPLOY_BIN).prev` exists on the server
   (errors out cleanly if not — e.g. right after a fresh install with no
   prior deploy).
4. **Store guard** (`scripts/storage_format_guard.py rollback`; 05-storage
   §5S.3.7). Before anything is swapped:
   - **In-flight probe.** It runs `sudo -n /usr/bin/find
     $(DEPLOY_STATE_DIR) -maxdepth 3 -path
     '$(DEPLOY_STATE_DIR)/restore/*/inflight.json' -print`. Any output means
     a restore or reset swap is in flight: it refuses, and nothing overrides
     that. Boot the current build once to finish it. A staged intent that has
     not started (`restore/pending.json`) passes, because the older build
     rejects it at its own start. A failed probe (sudo asking for a password,
     a wrong `DEPLOY_STATE_DIR`) refuses and names the command.
     `make rollback-restore` runs the same probe.
   - **Levels.** It reads the store's levels from
     `$(DEPLOY_DB).storage-levels` and asks `$(DEPLOY_BIN).prev
     --print-storage-levels` which levels it serves (one JSON object). A
     `.prev` older than that flag (R0) is asked `--print-storage-format`
     instead: format 1, or no such flag either, means every owner at level 0.
     Any other answer refuses. If any owner's store level is above what
     `.prev` serves, a binary swap cannot go back, and it refuses, naming
     `make rollback-restore` (below).
   - **A store no core build has opened** has no `.storage-levels`. Only then
     does the guard read HEAD's `$(DEPLOY_DB).storage-format`, as before.
     `ROLLBACK_IGNORE_FORMAT=1 make rollback` covers only an unreadable
     `.storage-format`, after you have checked the store is at format 1, and
     even then it asks the *current* binary for its format and refuses if
     that is above `.prev`'s or gives no answer. It never covers levels or the
     probe.
5. It asks `.prev` for `--print-capabilities` and, when `.prev` predates
   readiness notification, installs a `Type=simple` drop-in so systemd does
   not kill it at the start timeout (see
   [Rollback-safe unit type](#rollback-safe-unit-type)).
   Only then does it copy the *currently installed* (bad) binary to
   `$(DEPLOY_BIN).rolled-back` for forensics, restore `.prev` back to
   `$(DEPLOY_BIN)`, and restart `$(DEPLOY_UNIT)`. If the restart fails, the
   recipe goes on to the wait, which reads the unit and names the cause. It
   waits for `/health` to report `"ready":true` (see
   [Readiness](#readiness-what-deploy-and-rollback-wait-for)). A `.prev`
   older than readiness reporting answers without a `ready` field, so the
   wait fails at once after a swap that did happen; check that server by
   hand.
6. Dry-run without touching any host:
   ```bash
   make -n rollback DEPLOY_HOST=test-host DEPLOY_BIN=/tmp/x DEPLOY_DB=/tmp/db DEPLOY_URL=https://192.0.2.10:8484
   ```

### Readiness: what deploy and rollback wait for

The server reports readiness in two places:

- **`/health` field `ready`** (also `/api/health`, `/api/v1/health`). It is
  `false` while the in-memory read layer warms up, which takes 130-200 s on
  prod, and `true` once reads are on the fast path. The HTTP status is 200
  either way, so liveness probes are unaffected.
- **systemd `READY=1`**, under a `Type=notify` unit. The server sends it once
  its fatal startup steps have passed and its HTTP port is bound, which is
  before the warmup, and sends `STOPPING=1` when shutdown begins. A port
  that is already taken now fails startup instead of leaving a process that
  looks alive. Steps before that which can legitimately take long (opening
  the store, which may replay a large WAL; each migration applied; opening
  the search index) each send `EXTEND_TIMEOUT_USEC` for 10 minutes as they
  start, so a long migration run is not killed at `TimeoutStartSec`, while a
  single step that hangs still is. Nothing extends the timeout on a timer.

`make deploy`, `make deploy-debug`, `make rollback` and `make
rollback-restore` run `scripts/deploy-wait-ready.sh $(DEPLOY_URL)
$(DEPLOY_READY_TIMEOUT) $(DEPLOY_HOST) $(DEPLOY_UNIT)` after the restart
(`DEPLOY_READY_TIMEOUT` defaults to 300, `DEPLOY_UNIT` to
`audiobook-organizer.service`). `DEPLOY_URL` defaults, when unset, to
`API_URL` with a trailing `/api/v1` removed, or else to `https://<DEPLOY_HOST
without user@>:8484`. Every 5 s the script:

- reads the unit over ssh: `systemctl show -p
  ActiveState,SubState,NRestarts,ExecMainStatus $(DEPLOY_UNIT)` (no sudo).
  - `activating` is progress. A first boot that migrates the store stays
    activating while its steps extend the start timeout, so the
    `DEPLOY_READY_TIMEOUT` seconds count from when the unit leaves
    `activating`.
  - `failed` or `inactive`, or `NRestarts` rising (a crash and an automatic
    restart), fails at once. A rise with exit status 75 is the restart the
    service asks for after staging a restore, so the wait goes on.
  - Exit status 78 is a refusal, not a crash: the binary will not open this
    store, and its last log line is `install <release> first`. The script
    says "install the release the log names first" and exits 78. The
    committed unit does not restart a 78 (`RestartPreventExitStatus=78`).
- once the unit is `active`, polls `$(DEPLOY_URL)/health` until the body says
  `"ready":true`, and prints the elapsed seconds. No answer, or
  `"ready":false`, keeps it polling. A 200 body with no `ready` field fails at
  once (exit 2), because that binary predates readiness reporting.

Without an ssh host (Docker, launchd: `deploy-wait-ready.sh URL TIMEOUT`) only
`/health` can be read, so the wait lasts at least `DEPLOY_READY_URL_TIMEOUT`
seconds (default 1800). Exit status: 0 ready, 1 not ready, 2 a bad answer,
78 the binary refused the store. `deploy` runs its version check only after
the wait succeeds.

#### Rollback-safe unit type

An older binary never sends `READY=1`, so under the `Type=notify` unit
systemd would kill it at `TimeoutStartSec` and restart it in a loop. The
committed recipes rule that out. Before every restart,
`scripts/deploy-match-unit-type.sh HOST BINARY` runs
`BINARY --print-capabilities` on the host:

- **The output has a `sd_notify` line** (any build with readiness
  notification). The script removes
  `/etc/systemd/system/audiobook-organizer.service.d/zz-rollback-type-simple.conf`
  if it is there.
- **Anything else.** An older build fails with "unknown flag" and prints
  nothing, and an unreachable binary prints nothing too. The script installs
  that drop-in, containing `[Service]` and `Type=simple`, so the binary runs
  as it did before.

Either way it then runs `systemctl daemon-reload`. `make rollback` runs it on
`$(DEPLOY_BIN).prev` before the swap, and the `deploy` in
`Makefile.local.example` runs it on the uploaded binary before the `mv`.
Being wrong in the fail-safe direction costs nothing. The drop-in keeps the
unit's `NotifyAccess=main`, so `NOTIFY_SOCKET` is still set, and a
notify-capable binary still sends `READY=1` and `STATUS=`. Under
`Type=simple` systemd accepts them, but they do not gate the start.

The script uses `sudo -n`, so the sudoers drop-in below needs its four
commands.

#### Your own `Makefile.local`

`Makefile.local` is gitignored, so it does not pick any of this up by itself.
Paste these two lines into its `deploy` (and `deploy-debug`) recipe. Put the
first after the binary is copied to the host and before the `ssh` that moves
it into place and restarts. Put the second after that `ssh`, in place of any
`sleep`. Replace `/home/USER/audiobook-organizer` with the path your `scp`
uploads to:

```make
	@bash scripts/deploy-match-unit-type.sh $(DEPLOY_HOST) /home/USER/audiobook-organizer
	@bash scripts/deploy-wait-ready.sh $(DEPLOY_URL) $(DEPLOY_READY_TIMEOUT) $(DEPLOY_HOST) $(DEPLOY_UNIT)
```

The first line also clears the `Type=simple` drop-in that a `make rollback`
to an older binary leaves behind. Without it, that drop-in stays in place and
the service keeps running as `Type=simple`. That is safe, but `systemctl
restart` no longer waits for the server's `READY=1`.

#### Installing the unit

**The owner installs the unit.** `deploy/audiobook-organizer.service` sets
`Type=notify`, `NotifyAccess=main` and `TimeoutStartSec=300`. No committed
target copies it to a host, but a local `deploy` recipe that copies it on
every deploy installs it on the next deploy. To install it by hand:

1. Deploy a binary that sends `READY=1` first (or run
   `scripts/deploy-match-unit-type.sh` before restarting). A new binary under
   the old `Type=simple` unit is fine; it simply has no socket to notify.
2. Copy the unit: `sudo cp deploy/audiobook-organizer.service /etc/systemd/system/`.
   Your `local.conf` drop-in keeps overriding `ExecStart`. It must start the
   binary directly, not through a wrapper shell, because `NotifyAccess=main`
   accepts the notification only from the main process.
3. Run `sudo systemctl daemon-reload`.
4. Run `sudo systemctl restart audiobook-organizer.service`.
   `systemctl restart` now returns once the port is bound, and
   `systemctl status` shows the server's status line.

To undo by hand, put `Type=simple` back (and drop the two added lines), then
run `daemon-reload` and restart. `make rollback` does not need this: it
installs the `Type=simple` drop-in itself when `.prev` is older.

### `make rollback-restore`: when a binary swap is refused

After a release that raised a store level (R2), `make rollback` refuses: the
store is in a shape `.prev` cannot read. The rollback is then an offline
restore plus the previous binary (05-storage §5S.3.12, 09 §9.2.1):

```bash
make rollback-restore                                      # the latest pre-upgrade checkpoint
make rollback-restore ROLLBACK_ARCHIVE=/path/on/host/x.tar.gz   # an archive, once the checkpoint is gone
```

It uses `DEPLOY_HOST`, `DEPLOY_BIN`, `DEPLOY_DB` and `DEPLOY_URL` as `rollback`
does, plus `DEPLOY_STATE_DIR` (default `/var/lib/audiobook-organizer`),
`DEPLOY_SERVICE_USER` (default `audiobook`), `DEPLOY_CHECKPOINT_DIR` (set it
to `core.storage.checkpoint_dir` if you set that; empty means the store's
parent) and `DEPLOY_OFFLINE_ENV` (the environment the restore runs with,
default `HOME` and `ABK_STATE_DIR` set to the state directory, as in the
committed unit). In order:

1. `RELEASE=<tag>` refuses: fetching a named release is F8's `updater
   fetch-release`, which this build does not have. Install that release's
   binary as `$(DEPLOY_BIN).prev` by hand instead.
2. **Read-only checks while the service still runs**, so a refusal leaves it
   running:
   - the in-flight probe (the same one `make rollback` runs, no override);
   - the source's levels: the latest checkpoint's record (`from_vector`,
     named by `$(DEPLOY_DB).migration-checkpoint`), or with
     `ROLLBACK_ARCHIVE` the levels of the archived store itself. For an
     archive, the current binary copies it into
     `<checkpoint dir>/.mediacore-checkpoints/scratch/rollback-restore-levels`
     with `storage restore-offline --into`, which writes the copy's own
     `.storage-levels`. The recipe reads that file and deletes the copy,
     which holds the store's secrets, whatever the outcome.
   - `.prev --print-storage-levels` must **equal** the source's levels. A
     `.prev` that serves less would refuse the restored store, and one that
     serves more would migrate it again. On a mismatch it refuses and names
     the release to install as `.prev`: the checkpoint's `from_release`, or
     for an archive the release you ran when it was taken (HEAD's archives
     record no version).
   - It prints the source's time and warns that everything written since is
     lost, **including every credential change**: a key revoked, a user
     disabled or a password changed since then is live again. Repeat those
     revocations once the rollback is up.
3. It stops the unit and checks that it **stays stopped** (`inactive` or
   `failed`), then runs the in-flight probe again.
4. The **current** binary runs `storage restore-offline --checkpoint <name>
   --accept-loss` (or `--archive <file> --accept-loss`) as
   `DEPLOY_SERVICE_USER`, never as root, so every file keeps the service's
   ownership. The command takes the store's swap lock itself and refuses while
   a service is running or starting. The recipe does not retry: it stops,
   reports that the unit is stopped and the binary unchanged, and you find
   whatever started the unit. Afterwards it checks again that the unit stayed
   stopped.
5. It matches the unit type to `.prev`, keeps the current binary as
   `$(DEPLOY_BIN).rolled-back`, installs `.prev`, starts the unit and waits
   as above.

The unit is not masked during the restore. `systemctl mask` refuses a unit
whose file is installed in `/etc/systemd/system`. The restore's swap lock is
what excludes a concurrent start, and the two "still stopped" checks report
one that happened anyway. `Restart=on-failure` does not restart a unit that
`systemctl stop` stopped.

A checkpoint restore boot whose migration failed (journal `failed`) leaves
`inflight.json` behind, and both recipes refuse. The way out is by hand, with
the service stopped: run the **previous** binary's `storage restore-offline
--checkpoint <name>`, which is the one command admitted while
`inflight.json` exists, then start the service (09 §9.2.1).

### sudoers for `make rollback` (owner action)

`make rollback` and `make rollback-restore` run every privileged step over a
plain `ssh HOST 'cmd'`, which has no tty, so `sudo` cannot prompt for a
password. Every read uses `sudo -n` (fail at once instead of hanging), because
the store's parent directory and the state directory are not readable without
sudo. Without passwordless rules for exactly these commands, every rollback
refuses and names the command it could not run, and the swap itself would fail
too. `scripts/deploy-match-unit-type.sh` (run by
`rollback` and by the template's `deploy`) adds four more commands: the
`mkdir`, `tee` and `rm` of the `Type=simple` drop-in, and
`systemctl daemon-reload`.

Add a drop-in with `sudo visudo -f /etc/sudoers.d/audiobook-organizer-rollback`
on the deploy host. Replace `deploy` with the ssh user, `audiobook` with
`DEPLOY_SERVICE_USER`, and the paths with the real `DEPLOY_DB`, `DEPLOY_BIN`,
`DEPLOY_STATE_DIR` and checkpoint directory values (sudoers needs literal
paths, so no variables). The first block is `make rollback`'s; the second
adds what `make rollback-restore` needs:

```
deploy ALL=(root) NOPASSWD: /usr/bin/cat /path/to/audiobooks.pebble.storage-format, \
    /usr/bin/cat /path/to/audiobooks.pebble.storage-levels, \
    /usr/bin/cat /path/to/audiobooks.pebble.migration-checkpoint, \
    /usr/bin/find /var/lib/audiobook-organizer -maxdepth 3 -path /var/lib/audiobook-organizer/restore/*/inflight.json -print, \
    /usr/bin/cp /usr/local/bin/audiobook-organizer /usr/local/bin/audiobook-organizer.rolled-back, \
    /usr/bin/cp /usr/local/bin/audiobook-organizer.prev /usr/local/bin/audiobook-organizer, \
    /usr/bin/systemctl restart audiobook-organizer.service, \
    /usr/bin/mkdir -p /etc/systemd/system/audiobook-organizer.service.d, \
    /usr/bin/tee /etc/systemd/system/audiobook-organizer.service.d/zz-rollback-type-simple.conf, \
    /usr/bin/rm -f /etc/systemd/system/audiobook-organizer.service.d/zz-rollback-type-simple.conf, \
    /usr/bin/systemctl daemon-reload

deploy ALL=(root) NOPASSWD: /usr/bin/systemctl stop audiobook-organizer.service, \
    /usr/bin/systemctl start audiobook-organizer.service
deploy ALL=(audiobook) NOPASSWD: /usr/bin/cat /path/to/.mediacore-checkpoints/*.json, \
    /usr/bin/cat /path/to/.mediacore-checkpoints/scratch/rollback-restore-levels.storage-levels, \
    /usr/bin/rm -rf /path/to/.mediacore-checkpoints/scratch/rollback-restore-levels /path/to/.mediacore-checkpoints/scratch/rollback-restore-levels.state /path/to/.mediacore-checkpoints/scratch/rollback-restore-levels.storage-levels /path/to/.mediacore-checkpoints/scratch/rollback-restore-levels.storage-format, \
    /usr/bin/env HOME=/var/lib/audiobook-organizer ABK_STATE_DIR=/var/lib/audiobook-organizer /usr/local/bin/audiobook-organizer storage restore-offline --db /path/to/audiobooks.pebble *
```

The `find` line names the probe exactly as the recipes run it; sudo compares
the arguments after the shell removed the quotes around the `-path` pattern,
so the line has none. The checkpoint record's name changes with every
upgrade, so its line ends in a wildcard; it runs as the service user, which
can already read those files. The `restore-offline` line ends in a wildcard
for the source (`--checkpoint <name> --accept-loss`, `--archive <file>
--accept-loss`, or `--archive <file> --into <scratch copy>`). It too runs as
the service user, never as root.

Check it with `ssh HOST 'sudo -n cat /path/to/audiobooks.pebble.storage-levels'`.
It should print `<owner> <level>` lines and not ask for a password. Use
`command -v cat cp find env rm systemctl` on the host if those binaries live
somewhere other than `/usr/bin`.

Note this only preserves **one** prior version — a second consecutive `make
deploy` overwrites `.prev` with the (now second-to-last) binary. If you need
deeper history, keep your own timestamped copies (see `make backup` in
`Makefile` for the equivalent pattern applied to data directories).

## 3. Windows GPU Ollama keepalive (`scripts/manage-ollama-windows.py`)

The Windows GPU box (`192.168.0.20`, reached via the `windows-gpu` SSH alias —
see `scripts/setup-ssh-from-mac.sh` for creating that alias) runs Ollama
serving `bge-m3` (1024-dim embeddings) and `qwen2.5:7b-instruct` (LLM).
Commands are sent as base64-encoded (UTF-16LE) PowerShell via
`ssh windows-gpu powershell -NoProfile -EncodedCommand ...`, because a
scp'd `.ps1` mis-parses over that SSH path (documented in the status doc
cited above).

```bash
uv run scripts/manage-ollama-windows.py --status         # report loaded models
uv run scripts/manage-ollama-windows.py --setup           # install, firewall, register task, pull models
uv run scripts/manage-ollama-windows.py --install-task    # register OllamaServe only
uv run scripts/manage-ollama-windows.py --restart         # kill + relaunch ollama serve
```

`--setup` and `--install-task` register a Windows Scheduled Task named
**`OllamaServe`**, bound to an interactive logon session
(`New-ScheduledTaskPrincipal -LogonType Interactive`). This is the actual
keepalive mechanism OPS-2 flagged as undocumented — recreating the
install/pull steps without registering this task would leave the
reproducibility gap open.

### Residual risk (OPS-2 — not closed by this script)

`OllamaServe` is bound to an interactive logon session because `ollama
serve` needs GPU access that is unavailable in a headless/service context —
plain `ollama serve` over SSH or a "run whether user is logged on or not"
service dies or loses GPU access. **Do not** switch the task to headless
mode to "fix" this; that plausibly breaks the GPU access the interactive
binding exists for. Practical consequence: a logoff, reboot, or Windows
Update can still kill the Ollama process before the next interactive logon,
and nothing currently pages an operator when that happens. A periodic
reachability probe (e.g. hitting `/metrics` or `--status` on a schedule) is
explicitly **out of scope** for this task — treat it as a follow-up
hardening item, not something silently folded in here.

## Cross-references

- [`docs/consultancy/06-process-and-security.md`](../consultancy/06-process-and-security.md) — OPS-1, OPS-2, OPS-6 findings this document addresses.
- [`docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md`](../archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md) — origin of the Windows GPU setup and the `-EncodedCommand` gotcha.
- [`runbooks.md`](runbooks.md) — general deploy runbook and systemd service management.
