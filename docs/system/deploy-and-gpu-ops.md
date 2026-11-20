<!-- file: docs/system/deploy-and-gpu-ops.md -->
<!-- version: 1.22.0 -->
<!-- guid: d5e7f9a1-b3c5-4d7e-9f1a-3b5c7d9e1f3a -->
<!-- last-edited: 2026-11-18 -->

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
$EDITOR deploy/local.conf       # set WHISPER_REMOTE_URL and the other Environment= lines
```

`deploy/local.conf` holds only the service's `Environment=` lines. `deploy`
renders them into the systemd drop-in
`/etc/systemd/system/audiobook-organizer.service.d/local.conf` through the
`install-staged` helper (see "Rollback helpers" below, and
[`runbooks.md`](runbooks.md#systemd-service) for the existing deploy runbook).
Every other setting of the host (the `ExecStart=` command line with its TLS
and DB paths, `TimeoutStopSec=`, `WorkingDirectory=`, `MemoryMax=`) lives in
`host.conf` beside it, which the owner writes once as root and no deploy
touches; see "host.conf: the settings deploy does not write" below.
`Makefile.local` is picked up automatically by the committed `Makefile` via
`-include Makefile.local`.

## 2. Rollback flow

`Makefile.local.example`'s `deploy:` recipe installs the uploaded binary
through the `install-staged` helper (see
[Rollback helpers](#rollback-helpers-owner-action)), which keeps the
previously deployed binary as `$(DEPLOY_BIN).prev` before it replaces it:

```
ssh $(DEPLOY_HOST) 'sudo -n $(DEPLOY_HELPERS)/install-staged binary '"$(cat dist/<binary>.generation-args)"
```

`install-staged unit` and `install-staged local-conf` run first in the same
recipe, and each captures the unit or drop-in it is about to replace. When
`install-staged binary` rotates `.prev`, it commits those captures as one
**generation**, named by the sha256 of the binary it replaced, so the previous
binary, unit and `local.conf` are kept together and a rollback can put all
three back (see
[The service environment is part of a rollback](#the-service-environment-is-part-of-a-rollback)).
The four words after `binary` are what `scripts/deploy_generation_args.py`
measured about the new build (its sha256, version, store levels and whether the
retired-name list is enforced in it); the recipe runs it right after the upload,
before anything on the host changes.

A `Makefile.local` whose `deploy` replaces the binary with `sudo mv` or
`sudo cp` keeps none of this, and `make deploy` refuses it
(`make deploy-env-check`, below) until it is synced from
`Makefile.local.example`.

Once your real `Makefile.local`'s `deploy` target installs through the helper
the same way, the flow is:

1. `make deploy` — builds, ships, and (via the line above) copies the
   currently-running binary to `$(DEPLOY_BIN).prev` on the server before
   installing the new one (atomically: a copy beside it, then a rename) and
   restarting the service. The unit and `local.conf` it replaced are kept as a
   generation in `$(DEPLOY_BIN).envgen/`, named by the replaced binary's sha256.
   `deploy-env-check` runs before anything is uploaded.
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
   right after a fresh install with no prior deploy). It then has the
   `install-staged rollback check` helper find and validate the kept
   environment for `.prev`: the generation whose sha256 is `.prev`'s own, never
   the newest or the one beside it. It refuses, before anything is stopped or
   swapped, if there is none, or it is unreadable or not valid for the host,
   and the refusal lists what is kept (`make rollback-check` lists it any time).
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
   `InvocationID`, and only then does the `install-staged rollback apply`
   helper keep the *currently installed* (bad) binary as
   `$(DEPLOY_BIN).rolled-back` and install `.prev` as `$(DEPLOY_BIN)`
   atomically (a copy to `$(DEPLOY_BIN).new`, then a rename, which a running
   executable does not block), after putting the kept unit and `local.conf`
   back and ahead of a final `daemon-reload`. Then it restarts `$(DEPLOY_UNIT)`. A restart sudo refuses fails at
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

- **`/health` field `data.ready`** (the body is the `{"data": {...}}` envelope every API route uses). It is
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
line. Drop any `scp` of `deploy/audiobook-organizer.service`: the unit is
installed from the helper bundle, not from an upload. A `sudo cp` of the unit
or of `local.conf` into `/etc/systemd/system` is not granted, and must not
be: systemd reads those files as root, so whoever can write them can write
`User=root` or `ExecStartPre=` and is root.

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
`Type=notify`, `NotifyAccess=main` and `TimeoutStartSec=300`. It is part of
the helper bundle: the owner installs it root-owned into the helpers'
directory with the rest of the bundle ("Rollback helpers" below), and every
`deploy` runs `install-staged unit`, which checks `host.conf` (below) and
then copies that root-owned file into `/etc/systemd/system` unchanged. A change to the committed unit reaches the
host when the owner reinstalls the bundle, never through a deploy upload.
To install it by hand:

1. Deploy a binary that sends `READY=1` first (or run
   `scripts/deploy-match-unit-type.sh` before restarting). A new binary under
   the old `Type=simple` unit is fine; it simply has no socket to notify.
2. Copy the unit: `sudo cp deploy/audiobook-organizer.service /etc/systemd/system/`.
   Your `host.conf` drop-in keeps overriding `ExecStart`. It must start the
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
make rollback-restore RELEASE=v0.41.0                  # fetch that release and install it as .prev first
```

It uses `DEPLOY_HOST`, `DEPLOY_BIN`, `DEPLOY_DB`, `DEPLOY_UNIT` and
`DEPLOY_URL` as `rollback` does. Everything else (the state directory, the
service user, the checkpoint and archive directories, the environment the
restore runs with) comes from the helpers' config on the host.
`ROLLBACK_ARCHIVE` is a file name inside the config's `archive_dir`, never a
path. `scripts/deploy_rollback.py restore` runs it, in order:

1. **Read-only checks while the service still runs**, so a refusal leaves it
   running:
   - the helpers' config names this run's binary, store and unit, and
     `$(DEPLOY_BIN).prev` exists (not required with `RELEASE=`, below);
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
     that runs as the service user, which must own the state directory:
     `storage restore-offline` refuses `--into` under any other effective
     uid, as it does every offline write. `--into` never touches the live
     store and takes no swap lock, so this runs while the service is up.
   - `.prev --print-storage-levels` must **equal** the source's levels. A
     `.prev` that serves less would refuse the restored store, and one that
     serves more would migrate it again. On a mismatch it refuses and names
     the release to install as `.prev`: the checkpoint's `from_release`, or
     for an archive the release you ran when it was taken (HEAD's archives
     record no version). The refusal prints the command that fetches it:
     `make rollback-restore RELEASE=<that tag>`.
   - **`RELEASE=<tag>`** (09 §9.2.1, after an R2 hotfix: the tag you ran
     before the freeze) replaces the `.prev` you would otherwise install by
     hand. Once the source is read, the **current** binary runs
     `updater fetch-release --version <tag> --out <stage_bin>` on the host as
     the deploy user (05-updater §5U.4): it downloads that release's binary
     for this platform from the app's release source (https only, a redirect to
     http is refused too; no checksum or signature is checked in this build; the
     download is cut off after 30 s without data or 15 min in all, and the
     recipe gives the whole fetch `DEPLOY_FETCH_TIMEOUT` seconds, default 1200),
     runs it once with
     `--version-json` to check it is this app at that release, and renames it
     into place; it reads no store, so it runs with a scratch state directory
     and the service can stay up. The fetched file's
     `--print-storage-levels` must then **equal** the source's levels, exactly
     as for `.prev`. Only when they do does `install-staged prev` install it as
     `$(DEPLOY_BIN).prev`, **replacing any `.prev`**; the recipe then deletes
     the staged copy and reads `.prev`'s levels once more, refusing before the
     stop if they changed in between (a `make deploy` upload landing in
     `stage_bin`). Any refusal before the install (the fetch failed, an unknown tag, other
     levels, an earlier restore still running) deletes the staged copy and
     leaves `.prev` as it was. `RELEASE` must be a version tag
     (`v1.2.3`; `latest`, a URL or a path is refused), and `stage_bin` must be
     set in the helpers' config.
   - It prints the source's time and warns that everything written since is
     lost, **including every credential change**: a key revoked, a user
     disabled or a password changed since then is live again. Repeat those
     revocations once the rollback is up.
2. Still before the stop, it matches the unit type to `.prev` and reads the
   unit's `InvocationID`.
3. It stops the unit and checks that it **stays stopped** (`inactive` or
   `failed`; an unreadable unit is a state unknown and refuses too), then runs
   the in-flight probe again.
4. The **current** binary runs `storage restore-offline --checkpoint <name>
   --accept-loss` (or `--archive <archive_dir>/<name> --accept-loss`) through
   the `restore-offline` helper: as the service user (`systemd-run --uid`),
   never as root, so every file keeps the service's ownership, in the
   transient unit `audiobook-organizer-rollback-restore.service`. Its output
   is appended (never truncated) to
   `/run/audiobook-organizer-rollback/restore.log` after a `=== ... started
   <UTC time> ===` line, and relayed, so a dropped ssh connection neither
   kills the restore nor loses its output. The unit ends the log with
   `rollback helper: restore-offline finished: exit N` (or `signal N`) itself,
   so the result is on the host even when the connection, and the helper
   relaying it, are gone. The directory (0750) and the log (0640) stay
   root-owned, since systemd opens the log as root, and belong to the deploy
   user's primary group, so the deploy user reads them without `sudo`:
   `systemctl is-active audiobook-organizer-rollback-restore` and
   `tail -n 5 /run/audiobook-organizer-rollback/restore.log`, the two checks
   the recipe prints when the connection drops. A failed unit is not
   collected, so `systemctl status` keeps its result until the next restore
   resets it. A second restore refuses while the unit is in any state but
   `inactive` or `failed`: the recipe checks before it stops anything, and the
   helper again before it opens the log. The command refuses unless its
   effective uid owns the state directory (`offline_env`'s `ABK_STATE_DIR`),
   so `service_user` must be that owner. It takes the store's swap lock
   itself (an flock on `<state dir>/swap.lock` that never waits) and refuses
   while a service is running or starting. It also refuses the lock when the
   state directory is writable by group or others, or when `swap.lock` is not
   owned by the state directory's owner: only that owner creates the file,
   and no take changes its owner. The restore runs as the service user, so it
   passes on a host whose state directory is the service's own and not
   group- or other-writable. Nothing here runs `storage restore-offline`,
   `storage reset` or a read-write open of the store as root; the only root
   access to the state directory is `inflight-probe`'s read-only `find`. The
   recipe does not retry. Afterwards it checks again that the unit stayed
   stopped.
5. `install-staged rollback apply` keeps the current binary as
   `$(DEPLOY_BIN).rolled-back` and installs `.prev` atomically, with the unit
   and `local.conf` it ran under; the recipe then starts the unit and waits
   as above. There is no binary-alone helper: one would start the previous
   build under the new environment, which is what this exists to prevent.

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
  start on it; run `make rollback` again (the swap is idempotent: it replaces
  whatever is left and reloads systemd), then it starts the unit.

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
| `install-staged` | root | `binary`, `prev`, `unit`, `local-conf` or `rollback` | installs `stage_bin` as the binary (atomically, the old one kept as `.prev`) or, with `prev`, as `.prev` alone (`make rollback-restore RELEASE=`; the binary is not touched); copies the bundle's root-owned `audiobook-organizer.service` as the unit; or renders the `[Service]` and `Environment=` lines of `stage_local_conf` as the `local.conf` drop-in; `binary`, `unit` and `local-conf` then run `daemon-reload` (`prev` reloads nothing and does not delete `stage_bin`; the recipe does). `unit` and `local-conf` first capture what they replace, and `binary` (with nothing or four words of facts about the staged build) commits the capture as a generation in `<bin>.envgen/`, named by the replaced binary's sha256, when it rotates `.prev`; `rollback list`, `rollback check prev\|SHA256 [GEN]` and `rollback apply SHA256 [GEN]` read, validate and install a generation with `.prev` (below) |
| `pprof` | root | `on` or `off` | writes or removes the fixed drop-in `pprof.conf` (`Environment=ABK_PPROF_ADDR=localhost:6060`), `daemon-reload` |

`install-staged` never installs uploaded text as a unit file. systemd reads
unit files and drop-ins as root, so a file the deploy user can shape is a way
to `User=root` or `ExecStartPre=`; an earlier version checked uploaded files
against an allowlist of directives, and every check of that kind leaves a gap
where Python and systemd read a line differently. Instead:

- **`install-staged unit`** copies `audiobook-organizer.service` from the
  helpers' own directory, once host.conf passes its check ("host.conf" below;
  `install-staged local-conf` runs the same check). It refuses unless that file is root-owned, not
  writable by group or others, not a symlink, at most 256 KiB, and its
  `[Service]` section's `User=` and `Group=` equal the config's
  `service_user` and `service_group` and its `ExecStart=` runs the config's
  `bin`. Nothing the deploy user uploads is read.
- **`install-staged local-conf`** reads `stage_local_conf` and writes a new
  drop-in of its own: a fixed header, `[Service]`, and the file's
  `Environment=` lines. Comment and blank lines are dropped. Every other line
  must be `[Service]` or an `Environment=` line holding one assignment,
  `KEY=value` with `KEY` a shell-style name and `value` either free of
  whitespace, quotes and backslashes or wholly inside one pair of double or
  single quotes with no backslash. Anything else (`ExecStart=`, a second
  section, a continuation, two assignments on one line) refuses the deploy.
  So does a variable that makes the loader, libc, an interpreter or the Go
  runtime load or run something else, in any case and any quoting: `LD_*`,
  `MALLOC_*`, `PYTHON*`, `PERL5*`, `GCONV_PATH`, `GETCONF_DIR`,
  `HOSTALIASES`, `LOCALDOMAIN`, `RES_OPTIONS`, `NLSPATH`, `NODE_OPTIONS`,
  `GODEBUG`, `TZDIR`, `GLIBC_TUNABLES`, `BASH_ENV`, `SSL_CERT_FILE`,
  `SSL_CERT_DIR` and `LOCPATH`. This is defence in
  depth behind the drop-in rules below; `GODEBUG` is on the list because it
  can switch off Go's TLS and x509 checks.
  Every kept line may hold only printable ASCII and tabs, and a carriage
  return or NUL anywhere refuses: Python takes characters such as a vertical
  tab, `\x1c` or U+2028 for whitespace or line breaks and systemd does not, so
  a check that passed on them would not be checking what systemd reads.
  Only spaces and tabs are trimmed. Comments may hold any UTF-8 (they are
  never installed).
- **`install-staged binary`** and **`install-staged prev`** copy the staged
  binary through the open file descriptor they checked, never by name.

Every staged file is opened as root, so `install-staged` opens it with no
symlink in any component of its path (each directory from `/` with
`O_NOFOLLOW`), and refuses one that is not a regular file, has a second hard
link (a link to `/etc/shadow` would open) or is not owned by the deploy user
(`SUDO_UID`). A refusal names the line number and the rule, never the line's
text, so it cannot print a file the deploy user could not read.

#### host.conf: the settings deploy does not write

Since `local.conf` carries only `Environment=` lines, the rest of what it used
to hold moves to `/etc/systemd/system/audiobook-organizer.service.d/host.conf`,
which the owner writes once as root (`sudoedit` it, then
`sudo systemctl daemon-reload`). `deploy/local.conf.example` shows the block
in comments: the `ExecStart=` reset and the full command line with its TLS
and DB paths. Put `WorkingDirectory=`, `MemoryMax=` and any other
host-specific setting there too. Leave `TimeoutStopSec=` unset unless you
need more than the base unit's 60 s, which is sized to the server's stop
budget.

host.conf, and every other drop-in systemd applies to the unit, must run
nothing but the service user's `ExecStart=`: no other `Exec*=` line
(`ExecStartPre=`, `ExecStopPost=`, ...), an `ExecStart=` command that starts
with an absolute path after only the `@`, `-` and `:` prefixes (so no `+`,
`!`, `|` or any other) and holds one command (no `;` word, which starts a
second command whose prefix nothing checked; `\;` and a quoted `;` refuse
too), no `Type=oneshot` (which runs each of them), no
`PermissionsStartOnly=` or `AmbientCapabilities=`, and no `User=` or
`Group=` other than `service_user` and `service_group`. Every `Environment=`
line `local.conf` deploys reaches whatever the unit runs, and a deploy user
setting a variable is harmless only while that is the service user's own
binary, which the deploy user already controls. `install-staged unit` and
`install-staged local-conf` run one shared check of this before they copy
anything:

- host.conf must exist as a regular file (not a symlink), owned by root and
  not writable by group or others.
- Every drop-in systemd will apply is checked by the same rules and must be
  a regular file owned by root and not writable by group or others, in a
  directory with the same ownership. The list is what
  `systemctl show -p DropInPaths <unit>` reports (parsed strictly; a path
  it cannot parse refuses) together with a scan of every unit directory
  (`/etc/systemd/system`, `/run/systemd/system`, `/usr/lib/systemd/system`,
  the `.control`, `.attached`, transient and generator directories) for
  `<unit>.d/`, each dash-prefix directory (`audiobook-.service.d/`) and the
  type-wide `service.d/`, for the unit's name and for each alias
  (`systemctl show -p Names`, parsed strictly, and any symlink in a unit
  directory that points at the unit). The scan is not only a fallback: systemd reports
  only the drop-ins it loaded at its last reload, and both installs reload.
  The helper's own drop-ins (`local.conf`, `pprof.conf`, the `Type=simple`
  one) pass by construction. A masked drop-in (a symlink to `/dev/null`)
  refuses like any other symlink.
- The unit template in the helper bundle is checked by the same rules.
- So is the unit's main file wherever systemd finds it: what
  `systemctl show -p FragmentPath -p SourcePath <unit>` reports (parsed
  strictly) and every copy of `<unit>` in the unit directories (an `/etc`
  copy shadows `/usr/lib`, but each copy is checked). Each must also set
  `User=` and `Group=` to `service_user` and `service_group`, since a unit
  without `User=` runs as root, and run the configured binary. A generated
  unit (`SourcePath` set), a `FragmentPath` outside `/etc/systemd/system`,
  `/run/systemd/system`, `/usr/local/lib/systemd/system` and
  `/usr/lib/systemd/system`, or a copy in a generator, transient,
  `.control` or `.attached` directory refuses. With no unit file at all,
  `install-staged local-conf` refuses. `install-staged unit` skips the
  `/etc` copy, missing or not, since it is about to replace it with the
  checked template: a bad `/etc` copy never blocks the command that fixes
  it. Every other copy, such as a shadowed `/usr/lib` one, must still pass.

Lines are checked as systemd joins them: a line ending in an odd number of
backslashes continues onto the next, the last backslash becoming a space,
comment lines inside a continuation are skipped, and an empty line ends
one. So `ExecStart=\` followed by `+/bin/x` is a `+` prefix, a line ending
in `\\` (an even number) does not continue, and `Environment=A=b\`
followed by `User=...` sets no `User=` at all. Every check reads lines this
way: the rules, the `User=`/`Group=`/`ExecStart=` check of the template and
of each unit file (where the last assignment wins and an empty one resets
the setting, so a later empty `User=` means root), and the `local.conf`
renderer. A refusal names the file and the line, never the
text. So a host without a good host.conf never gets the
base unit installed, and can never restart on the base unit's `ExecStart`,
whether or not the deploy carries a `local.conf`.

Do this before the first deploy with these helpers. Until then
`install-staged unit`, the first install step, refuses, and the deploy stops
with nothing changed. If host.conf is good but `local.conf` still holds the
settings that belong in host.conf, `install-staged local-conf` refuses
instead. By then `deploy` has already reinstalled the unit from the helper
bundle and run `daemon-reload`, but the binary is untouched and the service
has not been restarted: it keeps running, and the previous `local.conf` is
still in place, so a later restart still uses its settings.

Install them on the deploy host, once, with the one installer the repo ships.
It is run by the owner, from a checkout of the commit being deployed, ON THE
DEPLOY HOST. The dry run runs from the checkout as you; `APPLY=1` exports `HEAD`
(not your working tree) to a root-owned directory, `/opt/aorg-install` (override
with `ROLLBACK_EXPORT_DIR=`), and runs the installer from there with `sudo`, so
the account needs `sudo` and the commit must be the one you reviewed:

```bash
make install-rollback-helpers ROLLBACK_INSTALL_ARGS="--deploy-user <ssh user> \
  --service-user <unit User=> --state-dir <state_dir> --archive-dir <archive_dir> \
  --stage-bin <stage_bin> --stage-local-conf <stage_local_conf>"      # shows the plan
make install-rollback-helpers APPLY=1 ROLLBACK_INSTALL_ARGS="..."    # installs it
```

It takes `DEPLOY_BIN`, `DEPLOY_DB` and `DEPLOY_UNIT` from `Makefile.local` and
the rest from `ROLLBACK_INSTALL_ARGS` (`--checkpoint-dir`, `--service-group` and
`--offline-env` are optional). Without `APPLY=1` it prints what it would do
(`create`, `replace`, `fix` or `unchanged` for each file) and changes nothing. With it
the installer:

1. renders the sudoers drop-in with `scripts/storage_format_guard.py sudoers`
   (the same table of commands the recipes run), writes it beside its final
   place under a name sudo ignores and checks it with `visudo -c`; a drop-in
   `visudo` refuses stops the install before a single file is written;
2. installs the bundle (`rollback_helper.py`, one stub per operation, mode
   0755) and the unit template, in a root-owned directory, atomically;
3. writes `/etc/audiobook-organizer/rollback.conf` (below), and only moves the
   sudoers drop-in into place last.

It is idempotent: a file already as it should be is `unchanged` and is not
rewritten, so running it again after a release that changes a helper replaces
exactly that helper. A `rollback.conf` that exists and differs is yours: it is
shown as a diff and left alone unless you pass `REPLACE_CONF=1`. It does not
write `host.conf` (below) and it never touches `Makefile.local`.

What the installer trusts, because it runs as root and writes what root later
executes:

- **What is already there is not trusted.** A file counts as `unchanged` only
  if its bytes, mode and owner are all right (and it has one link); a file the
  deploy user owns, or that group or others can write, is rewritten (`fix` when
  the bytes were already right, `replace` when they were not). An existing
  directory is chowned `0:0` and chmod-ed to `0755`. The sudoers drop-in is held
  to the same rule (mode `0440`, owner root). A `rollback.conf` with your bytes
  but the wrong mode has its mode fixed without `REPLACE_CONF=1`; your bytes are
  never touched.
- **It refuses, naming the path,** when any parent of
  `/usr/local/libexec/audiobook-organizer/rollback`, `/etc/audiobook-organizer`
  or `/etc/sudoers.d` is not root-owned or is writable by group or others, or
  when a directory it would fix is a symlink. Whoever can rename inside a parent
  can swap what the installer just made for a path of their own. Dry runs refuse
  too.
- **It will not run as root from a checkout that is not root's.** It loads
  `scripts/storage_format_guard.py` from the checkout as root and copies the
  helper out of it, so whoever can edit those gets root. There is no model of
  what one user can write (the owner of a file can chmod it): every path it
  reads, the directories `deploy`, `scripts` and the helper directory, and every
  directory above the checkout must be owned by root with no group or other
  write bit, no POSIX ACL and no symlink (a root-owned sticky directory such as
  `/tmp` above the checkout is skipped, but the checkout's own top directory,
  its child, must be root's). A `__pycache__` in those directories is held to
  the same rule, and the installer never reads one: it runs the guard and helper
  from their source bytes, with `python3 -I -B`. A normal checkout owned by the
  deploy user fails this check when the script is run directly: it refuses with
  `--apply` (or as root) and names the paths, and a dry run only prints a
  NOTICE. `make install-rollback-helpers APPLY=1` therefore never runs
  it from the checkout: it does `sudo install -d -o root -g root -m 755
  /opt/aorg-install`, `git -c tar.umask=0022 archive HEAD deploy/rollback-helpers
  deploy/audiobook-organizer.service scripts/storage_format_guard.py | sudo tar -x
  -f - --no-same-owner -C /opt/aorg-install` (the umask keeps the archive's modes
  from being group-writable, which a root `tar` would preserve). Files an earlier
  run left that this commit no longer has stay, and the directory is left on the
  host afterwards (nothing removes it; `sudo rm -r` it if you want it gone); the
  installer only reads the three paths above. Then `sudo python3 -I /opt/aorg-install/deploy/rollback-helpers/install_helpers.py ...
  --apply`. To run it by hand, do the same. The script re-runs itself with
  `python3 -I` either way. The recipe is covered by a `make -n` test and by
  extracting the same archive into a scratch directory and running the installer
  from it; the `sudo` steps themselves have not been run on a host.
- **The dry run's diff needs to read the installed drop-in.** It is mode 0440 and
  root-owned, so as an ordinary user the dry run says `unreadable` and shows the
  rendered text; run it with `sudo` to see the diff.
- **`--deploy-user` is required** (the drop-in grants that user, so it is never
  guessed from `$SUDO_USER`). The dry run prints `Sudoers grantee: <user>` and
  the rendered drop-in, or a diff against the installed one.
- After moving the drop-in into place it runs `visudo -c` over the **whole**
  sudoers configuration, and if that refuses it puts the previous drop-in back
  (or removes the new one) and says so.

**Order: this first, then sync `Makefile.local`.** The synced recipes call the
helper at their first step, so a `Makefile.local` synced before the bundle is
installed fails there.

The installer renders `/etc/audiobook-organizer/rollback.conf` from the values
given. To change it later, edit it as root (`bin`, `db`, `state_dir`,
`unit`, `service_user`, `archive_dir`, `checkpoint_dir` if
`core.storage.checkpoint_dir` is set, `service_group` if it differs from
`service_user`, and the deploy's staging paths `stage_bin` and
`stage_local_conf`, the files your `deploy` uploads with `scp`; a leftover
`stage_unit` is refused as an unknown setting). The helpers
refuse to run unless the file and its directory are root-owned and not
writable by group or others, and neither `service_user` nor `service_group`
may be `root`. The recipes read the file (it holds no
secret) and refuse when it names another binary, store or unit than
`DEPLOY_BIN`, `DEPLOY_DB` and `DEPLOY_UNIT`.

The installer writes the drop-in as `/etc/sudoers.d/audiobook-organizer-rollback`.
`python3 scripts/storage_format_guard.py sudoers --deploy-user <ssh user> --db
<DEPLOY_DB> [--bin <DEPLOY_BIN>] [--unit <unit>] [--service-user <user>]`
prints it with your values; it renders the same table of commands the recipes
run, and tests check against it every `sudo` command the rollback recipes run
and every `sudo` in `Makefile.local.example`'s `deploy`, `deploy-debug`,
`pprof-dropin` and `pprof-off` and in `scripts/deploy-match-unit-type.sh`. With the
placeholder values:

<!-- sudoers:begin -->
```
deploy ALL=(root) NOPASSWD: /usr/bin/systemctl stop audiobook-organizer.service, \
    /usr/bin/systemctl start audiobook-organizer.service, \
    /usr/bin/systemctl restart audiobook-organizer.service, \
    /usr/local/libexec/audiobook-organizer/rollback/inflight-probe "", \
    /usr/local/libexec/audiobook-organizer/rollback/restore-offline, \
    /usr/local/libexec/audiobook-organizer/rollback/unit-type, \
    /usr/local/libexec/audiobook-organizer/rollback/install-staged, \
    /usr/local/libexec/audiobook-organizer/rollback/pprof
deploy ALL=(audiobook) NOPASSWD: /usr/bin/cat /path/to/audiobooks.pebble.storage-levels, \
    /usr/bin/cat /path/to/audiobooks.pebble.storage-format, \
    /usr/bin/cat /path/to/audiobooks.pebble.migration-checkpoint, \
    /usr/local/libexec/audiobook-organizer/rollback/checkpoint-record
```
<!-- sudoers:end -->

A helper listed bare admits any arguments, which it validates; one listed
with `""` admits none. The plain lines have fixed arguments, which sudo
matches exactly. The three `cat` lines run as the service user, not root:
the sidecars sit in the store's directory, which the service user can write,
so one could be a symlink to any file root can read. No refusal prints what
a read returned, only its length and a hash, and names (owners, a release, a
time) only when they match a fixed grammar. `visudo -c` checks the syntax.

Check it with `ssh HOST 'sudo -n /usr/local/libexec/audiobook-organizer/rollback/inflight-probe'`:
it should print nothing (no swap in flight) and not ask for a password.

### The service environment is part of a rollback

A binary swap alone starts the previous build under the *current* unit and
`local.conf`. After an env-name change that is a build that does not read the
new names: a peer allowlist that fails open, no Cloudflare Access, no ABS JWT
or owner-email settings. So the deploy keeps the environment with the binary.

- **What is kept: generations.** `$(DEPLOY_BIN).envgen/` (root-owned, mode 0700)
  holds one JSON file (mode 0600) per replaced binary, named by that binary's
  sha256: the unit and the `local.conf` drop-in it ran under, plus what the
  deploy recorded about it (its version, the store levels it serves, whether the
  retired-name list was enforced in it) and a sequence number. They are written
  atomically (a file beside it, then a rename). `install-staged unit` and
  `local-conf` record what they replace in `pending.json` there, the first
  capture of a deploy wins, and a capture taken for a different binary than the
  one now installed is dropped. Only `install-staged binary` commits it, in the
  same step that rotates `.prev`, and it deletes the capture only after the new
  binary is in place: a deploy that fails half way, then runs again, still
  labels the OLD unit and `local.conf` as the old binary's. Redeploying the
  build that is already installed rotates nothing. A first deploy (no binary
  yet) keeps nothing.
- **How many, and which are never dropped.** Several, not one: the newest three,
  every *boundary* generation (never evicted, because the oldest is the pre-flip
  one; past ten the helper warns, since a deploy that crosses that many level
  steps is worth a look),
  and the newest three *unknown* pins. A generation is a boundary when the binary
  that replaced it serves a higher store level for any owner, when `enforced`
  differs between the two builds, or when that binary enforces the retired-name
  list and the one it replaced is not known to have: the last build before a
  level step, and the last build before the flip. After the flip every build is
  enforced; that is intended, and only the first enforced build marks a
  boundary. A boundary, once recorded, stays one: a rollback records the
  restored binary (and the facts of its own generation) as the installed one, so
  flip, rollback and flip again does not rewrite the pre-flip generation as an
  unknown with no facts, and a rewrite that has no new facts keeps the old ones.
  Boundary generations are never evicted for age, and no run of later deploys
  displaces them (they are rare). A generation is an unknown pin when a fact
  needed to tell is missing (a deploy whose recipe passes no facts, an ssh
  timeout while measuring): one more kept costs less than one lost, and those
  pins are bounded among themselves so the directory cannot grow. The helper
  cannot read the service-owned checkpoint area, so it cannot see whether the
  checkpoint taken at a level step is still kept. If any generation file is
  unreadable nothing is evicted, and eviction runs only after the new binary is
  in place. The deploy before the flip must go through the helpers with facts, so
  that the flip replaces a build with recorded facts; a first deploy through the
  helpers whose build enforces the list is a boundary too.
- **These files hold secrets.** A generation is a root-only plaintext copy of
  `local.conf` (the Cloudflare Access, ABS JWT, owner-email and peer settings)
  and of the unit. The directory is 0700 and the files 0600, owned by root; the
  helper refuses to read them if the directory or a file is open to group or
  others, or is a symlink. They are kept for as long as the generation is, so a
  secret rotated out of `local.conf` is still in the older generations: remove
  them with the generation, and treat the host's backups of this directory like
  `local.conf` itself. `make rollback-check` and the helper's messages never
  print their content.
- **One at a time, and durable.** `install-staged binary|prev|unit|local-conf`,
  and `rollback apply` hold one lock (`<bin>.envgen/lock`) for their
  duration and refuse, changing nothing, while another holds it. The renames they
  make are followed by an fsync of the containing directory.
- **`host.conf` is not in the snapshot.** It is written once by the owner and no
  deploy touches it, so a rollback restores the unit and `local.conf` but leaves
  `host.conf` as it is. The flip must therefore keep `host.conf` valid for BOTH
  builds (the previous release reads the same drop-in): do not rename or retire
  anything in it in the flip, and check it with the previous build's reading of
  it before the flip deploy. A rollback checks it the way a deploy does and
  refuses if it fails.
- **What a rollback does.** `make rollback` and `make rollback-restore` run
  `install-staged rollback check prev` before anything is stopped. It looks up
  the generation by the sha256 of the binary being restored and refuses, with
  the reason and the kept generations, when none exists or it is unreadable or
  not valid for the host (the kept unit and drop-in go through the same checks
  as a deploy's: `host.conf`, the template, the unit rules,
  `render_local_conf`). A rollback that cannot restore the environment is
  refused rather than done half. The swap (`install-staged rollback apply
  <sha256>`) is a compare-and-swap on `.prev`: it refuses if a deploy changed
  `.prev` after the check (the staged copy of `.prev` is hashed as it is copied,
  so a file swapped after the check is caught on the copy), stages the unit, the
  drop-in and the kept `<bin>.rolled-back` copy, renames the environment in first
  and the binary last, then runs `daemon-reload`. It can be run again after an
  interruption, or after a failed `daemon-reload` (run `make rollback` again: the
  deploy user has no sudo line for `systemctl daemon-reload`); a rerun does not
  replace `<bin>.rolled-back` once the binary is already the restored one. The helper never runs a binary as root.
- **`RELEASE=` and `ROLLBACK_ENV=<sha256>`.** `make rollback-restore RELEASE=<tag>`
  fetches a release, which essentially never has the sha256 of a build deployed
  from here, so no generation matches it. The environment is checked for the
  fetched file BEFORE it replaces `.prev` (a refusal leaves `.prev` and its
  environment exactly as they were), and the refusal lists the kept generations.
  To say which one the release needs, name it: `ROLLBACK_ENV=<sha256> make
  rollback-restore RELEASE=<tag>`. It is a generation's hash, not a switch: the
  recipe refuses when that generation's recorded store levels differ from the
  levels the binary serves. A generation kept without its facts (its build went
  in by hand, or the recipe passed none) has `levels=unrecorded` or
  `enforced=unrecorded`: nothing can be compared, so the recipe does not refuse
  but prints a WARNING naming what is missing, and the operator confirms the fit
  by hand. The `enforced` flag is compared too, when both sides are recorded:
  with no `RELEASE=` the binary being restored is `.prev`, whose own generation
  holds the flag of the build it was deployed as, and the recipe refuses an
  enforcing environment under a binary that predates the flip, and a pre-flip
  environment under an enforcing binary (the old names are ignored). A
  `RELEASE=` build has no generation of its own, so nothing can be compared: a
  generation of an enforcing build (`enforced=1`) gets a NOTE instead (it sets
  the new names, which a build that predates them does not read), and is
  confirmed by hand.
  `ROLLBACK_ENV=snapshot` (the old blind switch) is rejected.
- **`make rollback-check`.** Read-only: lists the generations (sha256, version,
  levels, pinned) and which one `.prev` matches, and prints no file content.
- **A `.prev` from before this change** (or one put there by hand, or by a
  `Makefile.local` that moves the binary with `sudo mv`) has no generation, and a
  rollback refuses until one deploy through `install-staged` has kept one.

### Before a deploy: `make deploy-env-check`

`make deploy` and `make deploy-debug` run `scripts/deploy_env_check.py` before
anything is uploaded or installed (`deploy-preflight.sh` runs it too, which a
`Makefile.local` that has not been synced from the example still calls). It
reads no host.

- **Retired names.** `deploy/local.conf`, and the `Environment=` lines of the
  unit template `deploy/audiobook-organizer.service` (every deploy installs a
  copy of it), may not set an env name in `deploy/retired-env-names.txt`: a binary with the config flip ignores a
  `warn` name with one boot warning (the setting silently stops applying) and
  refuses to boot on a `refuse` name. The list is generated from the rename
  table (`go test ./internal/books/cutover -run TestRetiredEnvList -update`
  rewrites it; the same test fails when it drifts). While its `enforced`
  marker is `false`, because the app does not yet hand the table to core and
  the binary still reads those names, the check reports and passes. The
  marker is derived from the source, so the change that wires
  `RetiredConfigNames` fails that test until the list is regenerated, and
  from then on the check refuses.
- **`Makefile.local`.** Its `deploy` and `deploy-debug` must install through
  `install-staged` (a `sudo mv` or `sudo cp` keeps no `.prev`, unit or
  `local.conf`). `DEPLOY_ALLOW_NO_ROLLBACK=1 make deploy` deploys anyway, with
  a warning, only while the list says `enforced: false`: for the build that
  carries the flip (`enforced: true`, or a list that cannot be read) it is
  refused, because the previous release reads names the flip retires and a
  deploy that keeps no rollback state strands it. The warning says which release
  a rollback then reaches: the older one an earlier deploy through the helpers
  kept beside `<bin>.prev`, skipping the one this deploy replaces; with none,
  `make rollback` refuses. A `Makefile.local` that installs the binary without
  the facts from `scripts/deploy_generation_args.py` still deploys, with a note:
  every kept generation then counts as an unknown pin (bounded to three).

### Owner steps this needs, once

Do them in this order. The first one is a command, not a file to put in place.

1. **Install the helper bundle, the sudoers drop-in and `rollback.conf`:**
   `make install-rollback-helpers` (it shows the plan), then
   `make install-rollback-helpers APPLY=1` (see "Rollback helpers" above for
   the arguments). Write `host.conf` once as root (below). `rollback.conf`'s
   `offline_env` must use the name the installed bundle's version reads
   (`ABK_STATE_DIR`; `ABO_CORE_PATHS_STATE_DIR` once the config flip is
   deployed), so a bundle reinstalled after the flip carries the new one.
2. **Then sync `Makefile.local`.** Merge `Makefile.local.example`'s `deploy` and
   `deploy-debug` recipes (and its `pprof-dropin` and `pprof-off` ones) into it,
   in place of yours. Keep your own `DEPLOY_HOST`, `DEPLOY_BIN` and the
   like. `make deploy-env-check` says whether it is in step. A private
   `deploy-preflight` target is fine: the check runs from
   `scripts/deploy-preflight.sh`, which it calls. The synced recipes call the
   helper at their first step: synced first, they fail there.

`make rollback-check` shows the host's state once both are done.

Until both are done `make rollback` and `make rollback-restore` refuse (no
helper, or no kept environment) rather than swap the binary alone, and
`make deploy` and `make deploy-debug` refuse a `Makefile.local` that deploys
with `sudo mv`. `DEPLOY_ALLOW_NO_ROLLBACK=1` deploys anyway, knowingly, but
only while `deploy/retired-env-names.txt` says `enforced: false`: the build that
carries the config flip cannot be deployed without rollback state, because the
previous release reads names the flip retires.

**Order of deploys.** Do both steps, then deploy once through the helpers
*before* the deploy that carries the config flip. That deploy keeps the
environment it replaces as a generation, and a rollback of the flip deploy
restores exactly that one, but only if it passes the unit checks: a unit file
from before the helpers (with its own `ExecStart=` rather than `host.conf`'s)
fails them, and the rollback then refuses instead of restoring it. A deploy
through the helpers installs the template unit, so the flip deploy's kept
environment is a valid one.

Note `.prev` is **one** prior binary — a second consecutive `make
deploy` overwrites it with the (now second-to-last) binary, and the environment generation of the one it replaced stays kept (above). If you need
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
