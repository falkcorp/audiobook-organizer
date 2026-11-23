<!-- file: docs/system/r2-cutover-runbook.md -->
<!-- version: 0.2.0 -->
<!-- guid: f41a3172-cd47-4776-9adf-e478758564c7 -->
<!-- last-edited: 2026-11-22 -->

> **DRAFT (R2-01b, second cut, 2026-11-22).** It records what the F3-07 deploy of 2026-11-22 taught us. The closing PR
> (R2-01c, after F8-04b) fills the `<...>` placeholders that still need a measured number or a release tag. Do not
> run this on a production host until that PR has merged and this banner is gone.

# R2 Cutover Runbook

This is the owner's procedure for taking an install to R2, the one-way cutover to the core platform, and for
keeping it or undoing it.

Placeholders, used everywhere below:

| Placeholder | Means |
|---|---|
| `<server>` | the production host, as your ssh config names it |
| `<abo-url>` | the service address you open in a browser |
| `<RC tag>` | the release candidate you deploy, for example `v0.240.0-rc.1` |
| `<archive>` | an archive file name |
| `<scratch>` | an empty directory you own, for example `~/r2-dryrun` |

**Two rules hold for the whole page.**

1. **No raw `sudo`.** Every privileged step goes through a `make` target that calls the root-owned rollback helpers
   (`deploy-release`, `rollback`, `rollback-restore`, `rollback-check`, `install-rollback-helpers`). If a step seems
   to need `sudo` and no target exists for it, stop: that is a gap, and the [gaps list](#known-gaps) names each one.
2. **Fixing forward is the default.** Going back after R2 has run throws away writes and brings back revoked
   credentials (see [Roll back](#roll-back)). Roll back only when R2 cannot be fixed in place.

Every step below has three parts: **Do** (the commands), **Check** (what you should see) and **Undo** (how to go
back from that step alone).

## What the F3-07 deploy already did (2026-11-22)

The production host has been migrated flip by flip. The F3-07 deploy on 2026-11-22 (settings into core, started
10:57 EST, health ready 11:28 EST) did these steps, so R2 records them and does not repeat them:

| Done on 2026-11-22 | How it was done | The check that proves it is still true |
|---|---|---|
| Env names renamed in `deploy/local.conf` to the new `ABO_*` names (20 renames, 3 true/false values, 3 comma lists turned into JSON lists, the allowed-emails list moved, `OAUTH_DEFAULT_ROLE` deleted). A copy of the old file was kept, mode 0600. | converted once by an agent, reviewed, then deployed | `make deploy-env-check` prints `deploy/local.conf sets no retired env name.` |
| `Makefile.local` switched to the helper recipes (`deploy` and `deploy-debug` install through `install-staged`). | merged from `Makefile.local.example` | `make deploy-env-check` prints `Makefile.local's deploy recipes go through the install-staged helper.` |
| The rollback helper bundle and its sudoers file installed on `<server>`. | you ran the installer on 2026-11-21 | `make rollback-check` lists the kept environment generations without a sudo error. |
| `host.conf` written beside the unit (the `ExecStart=` line with its flags, `WorkingDirectory=`, `MemoryMax=`, `TimeoutStopSec=`). | you ran a one-off script on 2026-11-21; a tracked make target for it is a [gap](#known-gaps) | `ssh <server> systemctl show -p DropInPaths audiobook-organizer.service` lists `.../audiobook-organizer.service.d/host.conf`. |

What it taught us about boot time:

- **Boot 1 was killed by systemd.** The unit is `Type=notify` with a 5 minute start timeout, and the app asks for
  10 more minutes before each migration step. The store rewrite (M8) was one long step with no request inside it,
  so systemd killed the boot at 11:06:50, exactly 10 minutes after the last request. The restart resumed from its
  checkpoint; the rewrite then took 18.7 minutes and the boot finished.
- A stopgap drop-in (`TimeoutStartSec=infinity`) was added by an agent through the helper's copy rule so the
  second boot could finish. The real fixes are #4989 (merged: the rewrite asks for time as it makes progress) and
  #4991 (in review: every step before ready asks for time). The next helper deploy re-renders the drop-ins
  **without** the stopgap, which is safe once both fixes are in the binary.
- `DEPLOY_READY_TIMEOUT` was 4200 seconds (70 minutes) for that deploy, set in `Makefile.local`. That number is
  for the F3-07 boot only. A first boot from the pre-freeze release (Path B) runs every migration at once and takes
  longer; R2-01c fills in the measured number.

## Before you start: preconditions

Do not start until each line holds. The first two are checked by the agents and written into the R2-01c PR; you
only read them there.

| # | Precondition | Where you see it |
|---|---|---|
| P1 | The `<RC tag>` you will deploy contains #4989 and #4991 (start-timeout fixes) and every R2 PR, R2-01c included. | the R2-01c PR description |
| P2 | The start-deadline rehearsal check passed on that RC: the rehearsal boots it under systemd's start-timeout rule and `start_deadline_held` must be PASS. | the R2-01c PR description |
| P3 | The stopgap is gone: the unit has a finite start timeout again. | `ssh <server> systemctl show -p TimeoutStartUSec audiobook-organizer.service` prints `TimeoutStartUSec=5min`, not `infinity`. If it still says `infinity`, the last deploy predates the fixes: tell the agents before going on. |
| P4 | Enough free space for R2's first boot (it may take a checkpoint and rewrite ranges). | `ssh <server> df -B1 --output=avail <db parent dir>` must be larger than `ssh <server> du -sb <db dir>`. If not, free space first, or the boot halts before it writes anything. |
| P5 | The rollback helpers answer. | `make rollback-check` prints the generations list (no `sudo` password prompt, no refusal). |
| P6 | The rollback helpers are reinstalled with the export helpers, an `export_dir`, and the `ABO_CORE_PATHS_STATE_DIR` offline environment. **Do it before the prod-copy rehearsal**, not only before Step 7. The exact command is [P6 below](#p6-reinstall-the-rollback-helpers-owner-before-the-prod-copy-rehearsal). | `ssh <server> cat /etc/audiobook-organizer/rollback.conf` shows an `export_dir =` line and `ABO_CORE_PATHS_STATE_DIR=` in the `offline_env =` line, and no `ABK_STATE_DIR`. |

### P6: reinstall the rollback helpers (owner, before the prod-copy rehearsal)

Why it comes before the rehearsal: the flipped binary refuses `ABK_STATE_DIR` in every offline command, and the
rollback helpers' current `rollback.conf` sets exactly that name. Until this reinstall runs, `make rollback-restore`
and `make rollback-restore RELEASE=<tag>` fail on the production host (the restore and the release fetch both run the
flipped binary with that environment), and so would `make export-keyring`. A rollback you cannot run is no rollback,
so this is done first, not discovered at Step 7.

Run it as the owner, **on the deploy host**, in a checkout of the commit you are deploying. Every value comes from
the host's current `/etc/audiobook-organizer/rollback.conf` (read it first: `ssh <server> cat
/etc/audiobook-organizer/rollback.conf`): `<bin>`, `<db>`, `<unit>` (`DEPLOY_UNIT`, if not the default),
`<service_user>`, `<state_dir>`, `<archive_dir>`, `<stage_bin>` and `<stage_local_conf>` are the lines of the same
names; `<deploy user>` is the account you log in as; `<export_dir>` is new, a directory you create and own.

```bash
mkdir -m 700 <export_dir>
make install-rollback-helpers APPLY=1 REPLACE_CONF=1 DEPLOY_BIN=<bin> DEPLOY_DB=<db> \
  ROLLBACK_INSTALL_ARGS="--deploy-user <deploy user> --service-user <service_user> \
    --state-dir <state_dir> --archive-dir <archive_dir> \
    --stage-bin <stage_bin> --stage-local-conf <stage_local_conf> \
    --offline-env 'HOME=<state_dir> ABO_CORE_PATHS_STATE_DIR=<state_dir>' \
    --export-dir <export_dir>"
```

- `REPLACE_CONF=1` is required. Without it the installer leaves a `rollback.conf` that differs from the one it
  renders alone (and says so), so `export_dir` and the new `offline_env` never land. The installer writes the whole
  file from these arguments, so also pass `--checkpoint-dir <value>` and `--service-group <value>` if they have a
  value in the current file. A line with an empty value can be left out (the production file has both, empty;
  dropping them is harmless), and passing either flag bare fails. `DEPLOY_BIN` and `DEPLOY_DB` can be left out when
  `Makefile.local` sets them.
- The single quotes around `--offline-env`'s value are part of the line: make passes them to the shell, which hands
  the value over as one argument.
- The state directory is named only as `ABO_CORE_PATHS_STATE_DIR`. That works with a pre-flip binary only because
  the production host's `state_dir` is the compiled default, `/var/lib/audiobook-organizer`: a pre-flip build
  refuses (fail-closed, "core's state directory ... is not ABO's ...") when that variable differs from its own state
  directory, so on any other `state_dir` a pre-flip binary refuses. The release fetch (`make rollback-restore
  RELEASE=<tag>`) handles both generations on its own. See Known gap 5.

**Check:** `ssh <server> cat /etc/audiobook-organizer/rollback.conf` shows the `export_dir =` line and the `ABO_`
token in `offline_env`, and `make rollback-check` still lists the generations. `make export-install-secrets`
is not the check: it ignores the offline environment, so it passes while Step 7 would fail.

**Undo:** in a checkout of the previous commit, run the same command with two changes: leave out `--export-dir`, and
pass the host's previous `offline_env` explicitly:

```bash
make install-rollback-helpers APPLY=1 REPLACE_CONF=1 DEPLOY_BIN=<bin> DEPLOY_DB=<db> \
  ROLLBACK_INSTALL_ARGS="<the same arguments as above, without --export-dir>
    --offline-env 'HOME=<state_dir> ABK_STATE_DIR=<state_dir>'"
```

then `rmdir <export_dir>`. Both changes matter. The previous bundle's `rollback_helper.py` refuses every `ABO_*`
token in `offline_env` ("is not HOME=, ABK_STATE_DIR= or ABK_*"), and its installer does not check the value, so the
same line with the new `--offline-env` installs a `rollback.conf` that breaks every helper, `install-staged` and the
restore included. With the ABK value the file is the one the host had before this reinstall, byte for byte. The
sudoers drop-in loses the two export lines: the two export stubs stay on disk but are no longer granted.
`REPLACE_CONF=1` is required here too, because the new `rollback.conf` differs from the one the old bundle renders.
The cost of the undo: `ABK_STATE_DIR` is back, which the flipped binary refuses, so `make rollback-restore` and
`make rollback-restore RELEASE=<tag>` fail again on the flipped host.

## Which path are you on?

| You are on | Use |
|---|---|
| An install already migrated flip by flip, running a recent `main` build. **The production host is on this path.** | [Path A](#path-a-an-install-already-migrated-flip-by-flip) |
| Any other install, coming from the pre-freeze release (the tag in `ci/prefreeze_release.txt`) | [Path B](#path-b-an-install-coming-from-the-pre-freeze-release) |

Both paths use the same step numbers, Step 0 to Step 8, and both end at [Keep or roll back](#keep-or-roll-back).

## Path A: an install already migrated flip by flip

Earlier flip deploys spent most steps already. On the final day you **record** the spent ones and **do** Steps 1,
2, 3 (a check only), 4 and 7.

| Step | Where it was spent | What is left on the final day |
|---|---|---|
| Step 0, merges and identity rows | the F4-08 deploy (its boot refuses otherwise) | Record the counts from that deploy. |
| Step 1, off-host archive | none | **Do it.** |
| Step 2, dry run | each flip's rehearsal | **Do it.** It passes on Path A when it reports 0 pending steps and 0 quarantined rows (ruling O-1). |
| Step 3, env names | the F3-07 deploy, 2026-11-22 ([above](#what-the-f3-07-deploy-already-did-2026-11-22)) | **Check only**: later flips may retire more names. |
| Step 4, deploy the RC | none | **Do it.** |
| Step 5, webhook receivers | the F7-09 deploy | Record. |
| Step 6, rules and dashboards | the F7-09 and F8-04a deploys | Record; reload only if R2 changed them since. |
| Step 7, keyring export | **not recorded as done** | **Do it** after the R2 boot. |
| Step 8, re-send invites | the F4-08 deploy | Record. |

**Ruling O-2:** there is no second M8 rewrite. Store files keep leftovers from rows the later steps (M3 to M6)
retired, and the R2 release note says so (05-storage §5S.3.12 and §5S.3.13).

Order on the final day: preconditions, Step 1, Step 2, Step 3, Step 4, Step 7, the [smoke run](#smoke-run), then
[Keep or roll back](#keep-or-roll-back). The step sections below are shared with Path B; where Path A differs, the
step says so.

## Path B: an install coming from the pre-freeze release

Do every step, in order, Step 0 to Step 8. This is the whole cutover on one first boot, so Step 4's boot is much
longer than the F3-07 boot.

## The steps

### Step 0: merges and identity rows

**Do** (Path B; Path A records the F4-08 deploy's counts instead). On the pre-freeze release, finish or undo every
account merge that is still in progress (the Users page shows them, with an Undo button). Then list the identity
rows the cutover cannot convert, and fix or delete each one:

```bash
audiobook-organizer storage cutover-blockers list --out <file>
audiobook-organizer storage cutover-blockers fix <key> --from <file>
audiobook-organizer storage cutover-blockers delete <key> --confirm
```

`list --out <file>` writes the rows, credential hashes included, to a file only its owner can read: edit the value
there for `fix`, and delete the file once the fix is done. `delete` removes the row and the rows that go with it.
These commands open the live store offline, so the service must be stopped and they run as the service user; no
helper runs them yet ([gap 2](#known-gaps)). The production host does not need them: it is on Path A.

**Check:** Step 2's dry run reports 0 blocking rows and no merge in progress.

**Undo:** a merge you finished can be undone from the Users page while you are still on the pre-freeze release. A
deleted identity row is gone; that is why Step 1's archive comes first on Path B.

### Step 1: archive, off host

**Do.** In the web app, signed in as the owner, open **Settings → Library** and press **Create Backup**. Wait for
the new file to appear in the list, then copy it off the host. R2-01c pins the exact download button and command
once F8-04a's backups page is live; until then this is [gap 1](#known-gaps).

The archive alone cannot be restored without the install's key and config. Keep them with it:
`.encryption_key` and `<db parent>/config.yaml`. They are readable only by the service user, so copy them with the
helper, from your Mac:

```bash
make export-install-secrets
```

It leaves the files in `~/aorg-offhost/<UTC stamp>/` (mode 0700; override with `OFFHOST_DIR=<dir>`), checks their
size and sha256, and deletes the copies it made on the host. A `config.yaml` that does not exist is reported as
absent, which is normal. These files let whoever holds them decrypt the install's secrets: keep them with the
archive, off shared storage.

**Check:** the new archive is in the Library list with today's time, and your off-host copy has the same size.

**Undo:** nothing on the service changed. Delete the archive from the Library list if you do not want it.

### Step 2: dry run (mandatory)

**Do.** Run the R2 binary's dry run on a copy of the Step 1 archive. It needs linux/amd64, so run it on `<server>`
itself, as your own user, in a scratch directory you own:

```bash
ssh <server>
mkdir -p <scratch> && cd <scratch>
gh release download <RC tag> --repo falkcorp/audiobook-organizer --pattern 'audiobook-organizer_Linux_x86_64'
chmod +x audiobook-organizer_Linux_x86_64
./audiobook-organizer_Linux_x86_64 --version          # must print the RC version
cp <path to your copy of the Step 1 archive> ./<archive>
./audiobook-organizer_Linux_x86_64 storage migrate --dry-run --from-archive ./<archive>
```

It prints per-step counts, quarantined rows and durations. It reads the archive copy and writes nothing to the live
store.

**Check:**
- Path A: 0 pending steps and 0 quarantined rows (ruling O-1). Anything else: stop and look.
- Path B: no failure, no blocking identity row, no merge in progress, and every quarantined row explained. If not,
  go back to Step 0, then run the dry run again; or stop and write down why.

**Undo:** delete the scratch directory: `rm -rf <scratch>`. It holds a copy of production data, so do this when
you are done either way.

### Step 3: env names

**Do** (both paths). From your checkout, on `main`:

```bash
cp -p deploy/local.conf ~/local.conf.pre-r2 && chmod 600 ~/local.conf.pre-r2
make deploy-env-check
```

**Check:** it prints `deploy/local.conf sets no retired env name.` and `Makefile.local's deploy recipes go through
the install-staged helper.` If it names a retired name instead, rename it in `deploy/local.conf` to the successor
it names (the R2 release note's rename table lists them all) and run `make deploy-env-check` again. A retired name
that is still set refuses the new binary's boot, so never skip this check.

Path B only: the host's own unit drop-ins, compose `.env` or plist may also set old names. On this host class they
come only from `deploy/local.conf` and `host.conf`; `host.conf` holds no `Environment=` lines.

**Undo:** `cp -p ~/local.conf.pre-r2 deploy/local.conf`. Nothing reaches the host until Step 4.

### Step 4: deploy the RC

**Do.** Deploy the newest `v<X.Y.Z>-rc.<N>` prerelease that satisfies precondition P1. Write the tag down: Keep
cuts the release from it. Never deploy a local build or an untagged commit.

```bash
make deploy-release RELEASE=<RC tag> DEPLOY_READY_TIMEOUT=4200
```

`deploy-release` runs `deploy-env-check` first, has the current binary download the tag on `<server>`, checks the
downloaded file's own version, uploads `deploy/local.conf`, and installs the unit and binary through the helpers.
The binary it replaces is kept as `.prev`, with the unit and `local.conf` it ran under. 4200 seconds is the F3-07
figure; R2-01c replaces it with the measured R2 number (Path B needs more).

While it waits, you can watch the boot: `ssh <server> journalctl -u audiobook-organizer -f`.

**Check:**
- The target ends with the deployed version matching the RC.
- `ssh <server> <DEPLOY_BIN> --version` (the binary path from `Makefile.local`) prints the RC tag's version
  `v<X.Y.Z>-rc.<N>`. Any other string means a different binary is running: stop and roll back.
- `curl -s <abo-url>/health` reports `ready` true.
- The boot was not restarted by systemd: `ssh <server> systemctl show -p NRestarts audiobook-organizer.service`
  prints `NRestarts=0`. A restart means the start timeout fired again: tell the agents (P1 and P2 should have
  caught it).

**Undo,** pick by what happened:
- The new binary refused the store before changing it (exit 78, or the version check failed before any migration
  ran): `make rollback`. It swaps `.prev` back with its own unit and `local.conf`, and it refuses by itself if a
  store level step already ran.
- A level step ran (Path B always; Path A only if R2 carries a new one): `make rollback-restore`. See
  [Roll back](#roll-back) for what that costs.

### Step 5: webhook receivers

**Do** (Path B; Path A records the F7-09 deploy). Switch each webhook receiver to core's format: the
`X-Mediacore-Delivery` header and the timestamped `X-Mediacore-Signature`. The OIDC redirect URI does not change.

**Check:** smoke check 4 below (a webhook reaches its receiver).

**Undo:** switch the receiver back, and only after a rollback.

### Step 6: rules and dashboards

**Do** (Path B; Path A only if R2 changed them since F8-04a). Reload the Prometheus rules and Grafana dashboards R2
ships under `deploy/prometheus/` and `deploy/grafana/`. After F8-04a, `/metrics` needs an API key holding the
`core:metrics.read` scope, so check that the scrape key in your Prometheus config holds it.

**Check:** smoke check 5 below (the scrape answers and the alerts evaluate).

**Undo:** reload the previous release's files from the same directories at the previous tag.

### Step 7: keyring export

**Do** (both paths; not recorded as done on Path A). After the R2 boot, write the keyring to the off-host place
where you keep backups:

The keyring export reads the live install's secrets, so it runs as the service user; do not type it yourself
(`sudo -u` is what the helper replaces). The helper runs `audiobook-organizer secrets export-keyring --out <file>`
for you, mode 0600. From your Mac, with the service running:

```bash
make export-keyring
```

It runs the command through the rollback helper and brings the file to `~/aorg-offhost/<UTC stamp>/` (override
with `OFFHOST_DIR=<dir>`), checks its size and sha256 and deletes the host's copy. It needs the helper bundle
reinstalled with the export helpers, `export_dir` and the ABO offline environment (precondition P6).

**Check:** the target prints one line, `<local path>  <bytes> bytes  sha256 <hash>`, then `The remote copies are
deleted.`, and health stops warning `secrets_keyring_not_exported`. The warning alone proves nothing: the binary
records the export before the file is delivered, so it clears even when the copy fails.

**Undo:** nothing on the service changes. Delete the file if you wrote it to the wrong place.

### Step 8: re-send invites

**Do** (Path B; Path A records the F4-08 deploy). Every open invite link stops working at R2. Re-send each open
invite from the Users page.

**Check:** the invite list shows the new send time.

**Undo:** none needed. A re-sent invite replaces the old link.

## Smoke run

**Ruling O-4:** a one-hour checklist on the RC. Run a full-day soak only if the RC carries code the production host
never ran.

1. Sign in to the web app.
2. An ABS phone app keeps its session and syncs progress.
3. A library scan and a scheduled task each run once, with no catch-up storm.
4. A webhook reaches its receiver.
5. The Prometheus scrape answers and the alerts evaluate.
6. Health shows no quarantined row.

## Keep or roll back

### Keep

**Do.** Cut the R2 production release from the RC tag you deployed, never from `main`:

```bash
gh workflow run release-prod.yml --ref <RC tag> -f skip-changelog-collect=true
```

`skip-changelog-collect` is needed because the changelog job only runs from `main` (ruling O-3, input added by
#4959). An agent PR on `main` folds the changelog fragments in afterwards.

Then, in order:
1. Write down the production tag the run creates. It names the same commit as the RC tag, and it is R3's upgrade
   floor (R3-02).
2. Prepend the R2 release note to the release's generated notes. Never replace them.
3. Take a new off-host archive (Step 1 again).
4. End the freeze:

```bash
gh variable set RELEASE_FREEZE --body ''
gh variable get RELEASE_FREEZE
```

**Check:** the second command prints an empty value. The release binaries are rebuilt at the RC's commit: the same
commit as what you smoked, not the same bytes.

**Undo:** before the freeze ends, delete the draft release and tag on GitHub if the run produced the wrong one, and
dispatch again.

**Ruling O-5:** the updater downloads goreleaser's raw asset name (`internal/updater.FetchOptions()`); F8-04b wires
it.

### Roll back

`make rollback-restore` stops the service, restores the pre-upgrade checkpoint with the current binary (as the
service user, through the restore-offline helper), puts `.prev` back with its own unit and `local.conf`, and starts
it. Its branches:

- **Checkpoint** (the default): `make rollback-restore`. The checkpoint is kept 7 days after it stops being needed;
  `storage prune-checkpoints` deletes it earlier.
- **Archive** (the checkpoint is gone): `make rollback-restore ROLLBACK_ARCHIVE=<archive>`, a file name inside the
  helpers' archive directory, not a path. It discards every write since that archive.
- **After a hotfix**: `make rollback-restore RELEASE=<tag>`, where the tag is the one in `ci/prefreeze_release.txt`
  at the R2 commit you deployed. That equals the checkpoint record's `from_release`, and it is the tag the recipe
  names when it refuses.
- It refuses while a swap is in flight. If a failed checkpoint boot left `<state dir>/restore/<id>/inflight.json`
  behind, stop and ask the agents: finishing that by hand means running the previous binary's
  `storage restore-offline --checkpoint <name>` as the service user, and no helper runs that form yet.

**Check:** `curl -s <abo-url>/health` reports `ready` true, and `--version` prints the previous release.

**What a restore costs:** every write since the upgrade (imports, edits, phone progress) is lost, and every
credential revoked, user disabled or password changed on R2 comes back. After a rollback, repeat those
revocations. This is why fixing forward is the default.

## Freeze hotfix

A production defect found between the start of the freeze and the R2 release is fixed by default in a release cut
from the pre-freeze release plus the fix, never from the R1 tag.

1. Branch from the tag in `ci/prefreeze_release.txt`, apply the fix, cut the production release from that branch,
   and deploy it as usual.
2. One agent PR on `main` replaces the tag line of `ci/prefreeze_release.txt` with the hotfix tag and re-runs the
   restore drill (`TestRollbackRestoreBootsPreviousBuild`) and `TestCutoverRefusedByOlderBuilds` against the hotfix
   binary.
3. The R2 build then embeds the hotfix tag as the checkpoint record's `from_release`, and that tag, not the
   original, is what `make rollback-restore RELEASE=` names.

On a host that already runs mid-R2 `main` (Path A, the production host), a defect is fixed forward on `main`
instead.

## Known gaps

Closed since the first draft: Keep from an RC tag (#4959, the `skip-changelog-collect` input) and a tracked target
that deploys a tagged RC (#4962, #4964, #4966: `make deploy-release`).

Still open:

1. **The off-host copy of the archive.** Before F8-04a the web app creates archives but has no download. R2-01c
   pins the download path once the core backups page is live.
2. **`storage cutover-blockers`, on Path B only (Step 0).** It runs offline as the service user with the service
   stopped. The key and config copy (Step 1: `make export-install-secrets`) and the keyring export (Step 7:
   `make export-keyring`) have their own helpers now; this one has none yet. Until it does, ask the agents rather
   than typing `sudo`. The production host is on Path A and does not need it.
3. **A tracked make target for `host.conf`.** It was written on 2026-11-21 with a one-off script. A tracked,
   tested target is coming as its own PR, so a rebuilt host can be set up the same way. Not needed for R2 on the
   current host.
4. **The measured R2 boot time.** R2-01c replaces the 4200 seconds in Step 4 with the measured number, and adds the
   Docker `start_period`.
5. **The state-directory variable is static in `rollback.conf`.** `ABO_CORE_PATHS_STATE_DIR` alone works with a
   pre-flip binary only because the production host's `state_dir` is the compiled default,
   `/var/lib/audiobook-organizer`. On a host with a different `state_dir`, a rollback to a pre-flip binary refuses
   (fail-closed: "core's state directory ... is not ABO's ..."); it does not open the wrong directory. The release
   fetch (`make rollback-restore RELEASE=<tag>`) already handles both generations: it retries once with
   `ABK_STATE_DIR` on exactly that refusal. What remains is the static name in `rollback.conf`; the general fix is a
   helper that picks the variable name by binary generation, scheduled after R2.

## Record

One tracking-issue comment, with no hosts, paths, secrets or values:

```
OWNER-05 recorded <date>
path: A | B; preconditions P1-P6: held
step 0: merges <n> finished/undone, blocking rows <n> fixed/deleted (Path A: from the F4-08 deploy)
dry run: done (<n> pending, <n> quarantined, <n> blocking); env check: clean
deployed RC <RC tag>, version string matched: yes; NRestarts=0: yes
webhooks switched <n>; keyring exported: yes; invites re-sent <n>; smoke run: done | skipped
decision: kept (R2 release <tag>, cut from <RC tag>; RELEASE_FREEZE cleared on <date>) | rolled back (<reason>)
```
