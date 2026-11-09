<!-- file: docs/system/deploy-and-gpu-ops.md -->
<!-- version: 1.9.1 -->
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

`Makefile.local.example`'s `deploy:` recipe installs the uploaded binary
through the `install-staged` helper (see
[Rollback helpers](#rollback-helpers-owner-action)), which keeps the
previously deployed binary as `$(DEPLOY_BIN).prev` before it replaces it:

```
ssh $(DEPLOY_HOST) 'sudo -n $(DEPLOY_HELPERS)/install-staged binary'
```

Once your real `Makefile.local`'s `deploy` target installs the binary the
same way, the flow is:

1. `make deploy` — builds, ships, and (via the line above) copies the
   currently-running binary to `$(DEPLOY_BIN).prev` on the server before
   installing the new one (atomically: a copy beside it, then a rename) and
   restarting the service.
2. If the new deploy is bad, run the committed rollback target:
   ```bash
   make rollback DEPLOY_HOST=192.0.2.10 DEPLOY_BIN=/usr/local/bin/audiobook-organizer DEPLOY_DB=/path/to/audiobooks.pebble DEPLOY_URL=https://192.0.2.10:8484
   ```
   (`DEPLOY_HOST`/`DEPLOY_BIN`/`DEPLOY_DB`/`DEPLOY_URL` are normally already
   set in your `Makefile.local`, so you can usually just run `make rollback`.
   `DEPLOY_DB` is the main Pebble store path on the server, the `--db` /
   `DATABASE_PATH` value; `DEPLOY_URL` is the server's base URL, which the
   readiness wait polls. `rollback` refuses to run without either.)
3. `rollback` (`scripts/deploy_rollback.py rollback`) first reads the
   rollback helpers' config on the host (`/etc/audiobook-organizer/rollback.conf`,
   see [Rollback helpers](#rollback-helpers-owner-action)) and refuses unless it
   names the same `DEPLOY_BIN`, `DEPLOY_DB` and `DEPLOY_UNIT`, then checks that
   `$(DEPLOY_BIN).prev` exists on the server (errors out cleanly if not, e.g.
   right after a fresh install with no prior deploy).
4. **Store guard** (`scripts/storage_format_guard.py`; 05-storage §5S.3.7).
   Before anything is swapped:
   - **In-flight probe.** The `inflight-probe` helper runs `find <state_dir>
     -maxdepth 3 -path '<state_dir>/restore/*/inflight.json' -print`. Any
     output means a restore or reset swap is in flight: it refuses, and
     nothing overrides that. Boot the current build once to finish it. A
     staged intent that has not started (`restore/pending.json`) passes,
     because the older build rejects it at its own start. A failed probe
     (sudo asking for a password, a wrong `state_dir` in the config) refuses
     and names the command. `make rollback-restore` runs the same probe.
   - **Levels.** It reads the store's levels from
     `$(DEPLOY_DB).storage-levels` and asks `$(DEPLOY_BIN).prev
     --print-storage-levels` which levels it serves (one JSON object). A
     `.prev` older than that flag (R0) is asked `--print-storage-format`
     instead: format 1, or no such flag either, means every owner at level 0.
     Any other answer refuses. If any owner's store level is above what
     `.prev` serves, a binary swap cannot go back, and it refuses, naming
     `make rollback-restore` (below).
   - **A store no core build has opened** has neither `.storage-levels` nor
     `$(DEPLOY_DB).migration-checkpoint`. Only then does the guard read HEAD's
     `$(DEPLOY_DB).storage-format`, as before. A store with a checkpoint
     pointer but no levels file was upgraded by a core build and lost the
     file: that refuses. `ROLLBACK_IGNORE_FORMAT=1 make rollback` covers only
     an unreadable `.storage-format`, after you have checked the store is at
     format 1, and even then it asks the *current* binary for its format and
     refuses if that is above `.prev`'s or gives no answer. It never covers
     levels or the probe.
5. It asks `.prev` for `--print-capabilities` and has the `unit-type` helper
   install a `Type=simple` drop-in when `.prev` predates readiness
   notification, so systemd does not kill it at the start timeout (see
   [Rollback-safe unit type](#rollback-safe-unit-type)). It reads the unit's
   `InvocationID`, and only then does the `swap-prev` helper keep the
   *currently installed* (bad) binary as `$(DEPLOY_BIN).rolled-back` and
   install `.prev` as `$(DEPLOY_BIN)` atomically (a copy to
   `$(DEPLOY_BIN).new`, then a rename, which a running executable does not
   block). Then it restarts `$(DEPLOY_UNIT)`. A restart sudo refuses fails at
   once: the old process is still the one serving. Any other restart failure
   goes on to the wait, which reads the unit and names the cause. The wait
   refuses to call the server ready while the unit still has the
   `InvocationID` read before the restart (see
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

- **`/health` field `data.ready`** (also `/api/health`, `/api/v1/health`;
  the body is the `{"data": {...}}` envelope every API route uses). It is
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
$(DEPLOY_READY_TIMEOUT) $(DEPLOY_HOST) $(DEPLOY_UNIT) <InvocationID>` after
the restart (the rollbacks call the same code from `scripts/deploy_rollback.py`;
`DEPLOY_READY_TIMEOUT` defaults to 300, `DEPLOY_UNIT` to
`audiobook-organizer.service`). `DEPLOY_URL` defaults, when unset, to
`API_URL` with a trailing `/api/v1` removed, or else to `https://<DEPLOY_HOST
without user@>:8484`. The last argument is the unit's `InvocationID`, read
(`systemctl show -p InvocationID --value`) before the restart or the stop.
Every 5 s the script:

- reads the unit over ssh: `systemctl show -p
  ActiveState,SubState,NRestarts,ExecMainStatus,InvocationID $(DEPLOY_UNIT)`
  (no sudo).
  - An `active` unit that still has the `InvocationID` read before the
    restart is the process that was already running: the restart never ran
    (sudo refused it, or ssh dropped before it reached systemd), and `/health`
    would report the OLD binary ready. The wait does not read `/health` then,
    and after `DEPLOY_RESTART_GRACE` seconds (default 30) it fails with "was
    not restarted".
  - `activating` is progress. A first boot that migrates the store stays
    activating while its steps extend the start timeout, so the
    `DEPLOY_READY_TIMEOUT` seconds count from when the unit leaves
    `activating`.
  - `failed` or `inactive`, or `NRestarts` rising (a crash and an automatic
    restart), fails at once. A rise of exactly one with exit status 75 is the
    restart the service asks for after staging a restore, so the wait goes
    on; more than one at once is a crash loop.
  - Exit status 78 is a refusal, not a crash: the binary will not open this
    store, and its last log line is `install <release> first`. The script
    says "install the release the log names first" and exits 78. The
    committed unit does not restart a 78 (`RestartPreventExitStatus=78`).
- once the unit is `active`, polls `$(DEPLOY_URL)/health` until `data.ready`
  in the body is `true`, and prints the elapsed seconds (a bare body with a
  top-level `ready` is read too). No answer, or `false`, keeps it polling. A
  200 body with no `ready` field in either place fails at once (exit 2),
  because that binary predates readiness reporting.

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

The drop-in is written or removed by the root-owned `unit-type` helper
(`unit-type simple` or `unit-type notify`, fixed content, nothing else), which
then runs `systemctl daemon-reload`. The rollbacks make the same decision
themselves on `$(DEPLOY_BIN).prev` before the swap (`make rollback-restore`
before it stops the service), and the `deploy` in
`Makefile.local.example` runs it on the uploaded binary before installing it.
Being wrong in the fail-safe direction costs nothing. The drop-in keeps the
unit's `NotifyAccess=main`, so `NOTIFY_SOCKET` is still set, and a
notify-capable binary still sends `READY=1` and `STATUS=`. Under
`Type=simple` systemd accepts them, but they do not gate the start.

The script uses `sudo -n` and the `unit-type` helper, granted in the
sudoers drop-in below. An earlier version wrote the drop-in with `sudo tee`
into the unit's drop-in directory; a sudo rule for that lets the deploy user
write any unit setting (`User=root`, `ExecStartPre=`) and so be root.

#### Your own `Makefile.local`

`Makefile.local` is gitignored, so it does not pick any of this up by itself.
Every `sudo` in it must become `sudo -n` with a command the drop-in below
grants: the binary, the unit and `local.conf` go through `install-staged`
(`binary`, `unit`, `local-conf`), the pprof drop-in through `pprof on|off`,
and the restart is `/usr/bin/systemctl restart <unit>`.
`Makefile.local.example`'s `deploy`, `deploy-debug` and `pprof-off` show each
line. A `sudo cp` of the unit or of `local.conf` into
`/etc/systemd/system` is not granted, and must not be: systemd reads those
files as root, so whoever can write them can write `User=root` or
`ExecStartPre=` and is root.

Paste these two lines into its `deploy` (and `deploy-debug`) recipe. Put the
first after the binary is copied to the host and before the `ssh` that moves
it into place and restarts. Put the second after that `ssh`, in place of any
`sleep`. Replace `/home/USER/audiobook-organizer` with the path your `scp`
uploads to:

```make
	@bash scripts/deploy-match-unit-type.sh $(DEPLOY_HOST) /home/USER/audiobook-organizer
	@bash scripts/deploy-wait-ready.sh $(DEPLOY_URL) $(DEPLOY_READY_TIMEOUT) $(DEPLOY_HOST) $(DEPLOY_UNIT) "$$inv"
```

The second line needs `inv`, the unit's `InvocationID` read before the
restart, in the same shell line as the restart and the wait;
`Makefile.local.example`'s `deploy` shows the whole line, including a sudo
refusal of the restart failing at once.

The first line also clears the `Type=simple` drop-in that a `make rollback`
to an older binary leaves behind. Without it, that drop-in stays in place and
the service keeps running as `Type=simple`. That is safe, but `systemctl
restart` no longer waits for the server's `READY=1`.

#### Installing the unit

**The owner installs the unit.** `deploy/audiobook-organizer.service` sets
`Type=notify`, `NotifyAccess=main` and `TimeoutStartSec=300`.
`Makefile.local.example`'s `deploy` uploads it, and `deploy/local.conf` when
present, and installs both through `install-staged`, which refuses either
file unless every directive in it is on the helper's allowlist (below), so
the next deploy installs it. To install it by hand:

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
make rollback-restore                                  # the latest pre-upgrade checkpoint
make rollback-restore ROLLBACK_ARCHIVE=batcave.tar.gz  # an archive in the helpers' archive_dir
```

It uses `DEPLOY_HOST`, `DEPLOY_BIN`, `DEPLOY_DB`, `DEPLOY_UNIT` and
`DEPLOY_URL` as `rollback` does. Everything else (the state directory, the
service user, the checkpoint and archive directories, the environment the
restore runs with) comes from the helpers' config on the host.
`ROLLBACK_ARCHIVE` is a file name inside the config's `archive_dir`, never a
path. `scripts/deploy_rollback.py restore` runs it, in order:

1. `RELEASE=<tag>` refuses: fetching a named release is F8's `updater
   fetch-release`, which this build does not have. Install that release's
   binary as `$(DEPLOY_BIN).prev` by hand instead.
2. **Read-only checks while the service still runs**, so a refusal leaves it
   running:
   - the helpers' config names this run's binary, store and unit, and
     `$(DEPLOY_BIN).prev` exists;
   - the in-flight probe (the same one `make rollback` runs, no override);
   - the source's levels: the latest checkpoint's record (`from_vector`,
     named by `$(DEPLOY_DB).migration-checkpoint`, read by the
     `checkpoint-record` helper as the service user), which must be
     restorable as core's `Checkpoint.Restorable()` says (`open` or `served`
     with no `delete_after`, or `completed`) and carry `from_vector.levels`;
     or with `ROLLBACK_ARCHIVE` the levels of the archived store itself. For
     an archive, the `restore-offline` helper's `into-check` checks free
     space, copies the archive into
     `<checkpoint dir>/.mediacore-checkpoints/scratch/rollback-restore-levels`
     with the current binary's `storage restore-offline --into` (which
     writes the copy's own `.storage-levels`), prints those levels and deletes
     the copy, which holds the store's secrets, whatever the outcome. All of
     that runs as the service user. `--into` never touches the live store
     and takes no swap lock, so this runs while the service is up.
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
3. Still before the stop, it matches the unit type to `.prev` and reads the
   unit's `InvocationID`.
4. It stops the unit and checks that it **stays stopped** (`inactive` or
   `failed`; an unreadable unit is a state unknown and refuses too), then runs
   the in-flight probe again.
5. The **current** binary runs `storage restore-offline --checkpoint <name>
   --accept-loss` (or `--archive <archive_dir>/<name> --accept-loss`) through
   the `restore-offline` helper: as the service user (`systemd-run --uid`),
   never as root, so every file keeps the service's ownership, in the
   transient unit `audiobook-organizer-rollback-restore.service`. Its output
   goes to `/run/audiobook-organizer-rollback/restore.log` and is relayed, so
   a dropped ssh connection neither kills the restore nor loses its output,
   and systemd refuses a second restore while one runs. The command takes the
   store's swap lock itself (an flock on `<state dir>/swap.lock` that never
   waits) and refuses while a service is running or starting. The recipe does not retry. Afterwards it checks again that the
   unit stayed stopped.
6. The `swap-prev` helper keeps the current binary as
   `$(DEPLOY_BIN).rolled-back` and installs `.prev` atomically; it starts
   the unit and waits as above.

Every failure after the first change says what state the host is in and
prints the commands that finish or undo the rollback:

- before the restore (the stop failed, the unit did not stay stopped, the
  second probe found a swap): nothing was restored, and the unit type
  matches `.prev`; it prints how to start the current binary again
  (`scripts/deploy-match-unit-type.sh HOST BIN`, then `systemctl start`);
- the restore failed: the same, plus what a "running or starting" refusal
  means;
- ssh dropped during the restore: it keeps running on the host; do not start
  the unit until `systemctl status audiobook-organizer-rollback-restore`
  shows it finished, then finish (swap and start) or start the current
  binary;
- the unit was running after the restore: the store **is** restored, and the
  current binary would migrate it again; stop it and run `make rollback`;
- the swap failed: the store is restored and the current binary must not
  start on it; finish with the `swap-prev` helper and `systemctl start`.

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

### Rollback helpers (owner action)

`make deploy`, `make deploy-debug`, `make pprof-off`, `make rollback` and
`make rollback-restore` run every privileged step over a plain `ssh HOST 'cmd'`, which has no tty, so `sudo` cannot prompt for a
password: every step uses `sudo -n` and needs a passwordless rule. Those
rules must not hand the deploy user root. In sudoers, a `*` in a command's
arguments also matches spaces and `/`, so a rule like `find <dir> -path
<dir>/restore/*/inflight.json -print` admits `find <dir> -path <dir>/restore/
-o -exec /bin/sh -c '...' ; -o -path /inflight.json -print`, a root shell, and
a wildcard on `cat` or on `restore-offline` lets the caller read any file or
restore from any archive. A rule without a wildcard can still be root: `sudo
cp <your file> /etc/systemd/system/<unit>` (or into `<unit>.d/`, or `sudo
tee` there) lets the caller write `User=root`, `ExecStartPre=+/bin/sh` or
`StandardOutput=file:/etc/sudoers.d/x` into a file systemd reads as root.
Regular expressions in sudoers need sudo 1.9.10,
newer than Ubuntu 22.04's. So every step that takes a variable argument goes
through a small root-owned helper that validates its own arguments, and the
drop-in grants each helper with no argument list and no wildcard.

The helpers are in `deploy/rollback-helpers/`: `rollback_helper.py` does the
work, and one stub per operation runs one fixed subcommand of it. Each refuses
any argument outside its fixed shape before it runs anything, builds the argv
of what it runs itself, never uses a shell, and takes every path from the
root-owned config:

| Stub | Runs as | Arguments | Does |
|------|---------|-----------|------|
| `inflight-probe` | root | none | the in-flight probe |
| `checkpoint-record` | service user | a checkpoint name (`20261101T020304Z-0badc0de` form) | prints that record, without following a symlink |
| `restore-offline` | root, the restore as the service user | `checkpoint NAME`, `archive FILE`, `into-check FILE` | `storage restore-offline` in a transient unit |
| `unit-type` | root | `simple` or `notify` | writes or removes the fixed `Type=simple` drop-in, `daemon-reload` |
| `swap-prev` | root | none | `.prev` installed atomically, the old binary kept |
| `install-staged` | root | `binary`, `unit` or `local-conf` | installs `stage_bin` as the binary (atomically, the old one kept as `.prev`), or `stage_unit` / `stage_local_conf` as the unit / its `local.conf` drop-in after checking every directive, then `daemon-reload` |
| `pprof` | root | `on` or `off` | writes or removes the fixed drop-in `pprof.conf` (`Environment=ABK_PPROF_ADDR=localhost:6060`), `daemon-reload` |

`install-staged unit` and `install-staged local-conf` read the staged file
once (no symlink, at most 256 KiB), check it, and install the bytes they
checked. Every directive must be on the allowlist in `rollback_helper.py`
(`ALLOWED`): the ones the committed unit and `local.conf.example` use, with
`User=` and `Group=` equal to the config's `service_user` and
`service_group` (the unit must set both: a unit without `User=` runs as
root), `ExecStart=` absolute with no `+` or `!` prefix, `StandardOutput=` and
`StandardError=` to the journal only, and `Wants=` limited to the network and
filesystem targets. Anything else (`ExecStartPre=`, `SupplementaryGroups=`,
`AmbientCapabilities=`, `EnvironmentFile=`, ...) is refused by name; add a
directive to `ALLOWED` only after checking it cannot run, write or grant
anything as root. A comment or blank line inside a continued value, and a
backslash followed by spaces, are refused, because systemd versions read them
differently.

Install them on the deploy host (as root):

```bash
install -d -o root -g root -m 0755 /usr/local/libexec/audiobook-organizer/rollback /etc/audiobook-organizer
install -o root -g root -m 0755 deploy/rollback-helpers/rollback_helper.py \
  deploy/rollback-helpers/inflight-probe deploy/rollback-helpers/checkpoint-record \
  deploy/rollback-helpers/restore-offline deploy/rollback-helpers/unit-type \
  deploy/rollback-helpers/swap-prev deploy/rollback-helpers/install-staged \
  deploy/rollback-helpers/pprof /usr/local/libexec/audiobook-organizer/rollback/
install -o root -g root -m 0644 deploy/rollback-helpers/rollback.conf.example /etc/audiobook-organizer/rollback.conf
```

Then edit `/etc/audiobook-organizer/rollback.conf` (`bin`, `db`, `state_dir`,
`unit`, `service_user`, `archive_dir`, `checkpoint_dir` if
`core.storage.checkpoint_dir` is set, `service_group` if it differs from
`service_user`, and the deploy's staging paths `stage_bin`, `stage_unit` and
`stage_local_conf`, the files your `deploy` uploads with `scp`). The helpers
refuse to run unless the file and its directory are root-owned and not
writable by group or others, and neither `service_user` nor `service_group`
may be `root`. The recipes read the file (it holds no
secret) and refuse when it names another binary, store or unit than
`DEPLOY_BIN`, `DEPLOY_DB` and `DEPLOY_UNIT`.

Add the drop-in with `sudo visudo -f /etc/sudoers.d/audiobook-organizer-rollback`.
`python3 scripts/storage_format_guard.py sudoers --deploy-user <ssh user> --db
<DEPLOY_DB> [--bin <DEPLOY_BIN>] [--unit <unit>] [--service-user <user>]`
prints it with your values; it renders the same table of commands the recipes
run, and tests check against it every `sudo` command the rollback recipes run
and every `sudo` in `Makefile.local.example`'s `deploy`, `deploy-debug`,
`pprof-dropin` and `pprof-off` and in `scripts/deploy-match-unit-type.sh`. With the
placeholder values:

<!-- sudoers:begin -->
```
deploy ALL=(root) NOPASSWD: /usr/bin/cat /path/to/audiobooks.pebble.storage-levels, \
    /usr/bin/cat /path/to/audiobooks.pebble.storage-format, \
    /usr/bin/cat /path/to/audiobooks.pebble.migration-checkpoint, \
    /usr/bin/systemctl stop audiobook-organizer.service, \
    /usr/bin/systemctl start audiobook-organizer.service, \
    /usr/bin/systemctl restart audiobook-organizer.service, \
    /usr/local/libexec/audiobook-organizer/rollback/inflight-probe "", \
    /usr/local/libexec/audiobook-organizer/rollback/swap-prev "", \
    /usr/local/libexec/audiobook-organizer/rollback/restore-offline, \
    /usr/local/libexec/audiobook-organizer/rollback/unit-type, \
    /usr/local/libexec/audiobook-organizer/rollback/install-staged, \
    /usr/local/libexec/audiobook-organizer/rollback/pprof
deploy ALL=(audiobook) NOPASSWD: /usr/local/libexec/audiobook-organizer/rollback/checkpoint-record
```
<!-- sudoers:end -->

A helper listed bare admits any arguments, which it validates; one listed
with `""` admits none. The plain lines have fixed arguments, which sudo
matches exactly. `visudo -c` checks the syntax.

Check it with `ssh HOST 'sudo -n /usr/local/libexec/audiobook-organizer/rollback/inflight-probe'`:
it should print nothing (no swap in flight) and not ask for a password.

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
