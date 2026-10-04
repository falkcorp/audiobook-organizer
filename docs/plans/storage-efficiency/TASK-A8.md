<!-- file: docs/plans/storage-efficiency/TASK-A8.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7deede34-2377-4ae5-89fb-9c515945c07d -->
<!-- last-edited: 2026-10-03 -->

# TASK-A8: Rebuild the rehearsal sandbox on an atomic ZFS clone

Wave W2, in parallel with A3 (different repository). Start only after A2
(`GET /diagnostics/db-census`) has merged to `main` and been deployed, because
the exit check compares A2's census on the sandbox with prod's. Executor: the
main session (it needs `sudo` on the server and owner judgement). Reviewer:
the owner.

This task edits **`falkcorp/infra-docs`** (a private repository), not
`audiobook-organizer`. Nothing in this brief may quote a private address:
use host role names ("the server") and paths only.

## 1. Goal and why

**Goal.** Make the dedup sandbox (`falkcorp/infra-docs`,
`scripts/dedup-sandbox/`) usable as the rehearsal box for the release B and C
cut-overs (plan B9, C9):

1. The DB image is an atomic ZFS snapshot of the dataset that holds the
   production app data, **cloned** to a sandbox dataset on the same pool. The
   clone replaces the rsync to a pristine copy and both `/tmp` copies. Reset
   is "destroy the clone and re-clone".
2. The instance runs with prod's `GOMEMLIMIT` and `GOGC`, inside
   `systemd-run --scope -p MemoryMax=<prod value>`, so a rehearsal measures
   memory against the same ceiling the cut-over will hit.
3. The existing unprivileged user and mount namespace and the media clone
   stay. The scheduler, operation resume, scans and outbound metadata and LLM
   fetchers are disabled for the run.
4. The July isolation test (`isolation-test.sh`) is re-run and passes.

For C9 only, the signal store will sit on the sibling dataset that C8a
chooses; this task leaves a clear place for a second clone but does not
create it.

**Why.** The sandbox was torn down on 2026-07-18. Its scripts snapshot
`/var/lib` (`sandbox-bootstrap.sh:20-23`), but prod app data moved to an NVMe
`.appdata` dataset on 2026-09-09, so the snapshot no longer holds the store.
They also copy the store into `/tmp/abk-sandbox/` (`sandbox-bootstrap.sh:22`,
`sandbox-reset.sh:19-20`), and `/tmp` is a 16 GB tmpfs against a store of
about 50 GB. They run with `GOMEMLIMIT=6GiB` (`sandbox-run.sh:49`), not
prod's value, and with no cgroup memory cap. The design (section 9) requires
every cut-over build to be started on this sandbox, against a clone of prod's
store datasets with prod's memory limits, before it reaches prod; the
downtime the owner approves is the time measured here.

## 2. Setup

```bash
cd /Users/jdfalk/repos/github.com/falkcorp/infra-docs
git fetch origin
git worktree add .worktrees/sandbox-zfs-clone -b feat/sandbox-zfs-clone origin/main
cd .worktrees/sandbox-zfs-clone
```

- Do NOT spawn subagents.
- Never edit the primary checkout of either repository.
- Commit work in progress every 15 minutes, and push it to your own branch.
- Every command that runs on the server runs over ssh as your normal user
  with `sudo -n` where the existing scripts already use it. Production is
  only ever READ: snapshots and clones are read-only to it.

## 3. Read before editing

- `infra-docs/docs/runbooks/dedup-sandbox.md`, all 181 lines: the isolation
  model, the privilege notes (`:45-66`), the isolation gate (`:67-90`), the
  replayable sequence (`:91-124`) and the open items (`:172-181`).
- `infra-docs/scripts/dedup-sandbox/sandbox-bootstrap.sh` (65 lines): the
  media snapshot (`:25-28`), the `/var/lib` snapshot (`:30-35`) and the rsync
  to the pristine copy (`:37-58`). The header (`:6-10`) explains why the copy
  existed: the store is owned by the service user, which is outside your
  subordinate uid range, so it cannot be uid-mapped into your user namespace.
- `infra-docs/scripts/dedup-sandbox/sandbox-reset.sh` (64 lines): media clone
  destroy and re-clone with retries (`:32-48`), divergence check (`:52-53`),
  data restore by `cp -a` from the pristine copy (`:55-58`, the copy at `:57`).
- `infra-docs/scripts/dedup-sandbox/sandbox-run.sh` (57 lines): the
  `unshare --user --map-root-user --mount` launch with two bind mounts
  (`:38-46`), the environment (`:48-50`) and the `serve` flags (`:51-56`).
- `infra-docs/scripts/dedup-sandbox/isolation-test.sh` (46 lines).
- `audiobook-organizer/deploy/audiobook-organizer.service:60-90`: the
  committed defaults for `GOMEMLIMIT`, `GOGC` and `MemoryMax`. The installed
  unit on the server is authoritative (it may differ); read it with
  `systemctl cat audiobook-organizer.service` and
  `systemctl show -p MemoryMax audiobook-organizer.service`.
- `audiobook-organizer/cmd/root.go:248-330`: `serve` startup, including the
  settings-encryption key in the constant secure state directory
  (`config.EnsureSecureStateDir`) and `guardAgainstKeyRegeneration`, which
  refuses to start rather than mint a new key.
- Design `docs/design/2026-10-03-storage-efficiency-design.md` (in
  `audiobook-organizer`), section 9, the rehearsal paragraph.

## 4. Re-verify anchors

Run in the `infra-docs` worktree. Line numbers are from `5602791`.

1. `grep -n 'VARLIB_DS=\|VARLIB_SNAP=\|PRISTINE=\|SNAP_DATA=' scripts/dedup-sandbox/sandbox-bootstrap.sh` → `20`, `21`, `22`, `23`.
2. `grep -n '^rsync -rlt' scripts/dedup-sandbox/sandbox-bootstrap.sh` → `43:`
3. `grep -n 'PRISTINE=\|WORK=' scripts/dedup-sandbox/sandbox-reset.sh` → `19`, `20`;
   `grep -nF 'cp -a "$PRISTINE" "$WORK"' scripts/dedup-sandbox/sandbox-reset.sh` → `57`.
4. `grep -n 'GOMEMLIMIT=6GiB\|GOGC=200\|exec unshare' scripts/dedup-sandbox/sandbox-run.sh` → `38`, `49`, `50`.
5. `grep -n "mount --bind" scripts/dedup-sandbox/sandbox-run.sh` → `40`, `41`.
6. On the server: `systemctl cat audiobook-organizer.service | grep -E 'DATABASE_PATH|GOMEMLIMIT|GOGC|MemoryMax'`
   gives the prod store path and limits. Then
   `zfs list -H -o name,mountpoint | sort -k2` and find the dataset whose
   mountpoint is the longest prefix of that store path. That dataset is what
   this brief calls the **app-data dataset**. Record its name, its pool and
   `zfs list -o space <dataset>` in the report. Do not assume a name.
7. In `audiobook-organizer` at the deployed commit: for each of the
   scheduler, operation resume at startup, library scans, and outbound
   metadata and LLM fetchers, find the switch that turns it off (a flag, an
   environment variable, or a persisted setting). Record file:line for each.
   If any of the four has no switch, stop at step 5 below and report it.
   Adding a switch is an application change and needs its own task in
   `audiobook-organizer`; do not add one here. Expect this: the July runbook
   (`docs/runbooks/dedup-sandbox.md`) does not mention the scheduler, resume
   or fetchers, and on `543827ef7` no `os.Getenv` in `internal/` or `cmd/`
   reads such a switch. If the switches are missing, A9's sandbox check and
   the B9/C9 rehearsals wait for that app-side task.

## 5. Steps

1. **`sandbox-bootstrap.sh`: snapshot the app-data dataset, no copy.**
   - Replace `VARLIB_DS`/`VARLIB_SNAP`/`SNAP_DATA` with `DATA_DS` (the
     app-data dataset from anchor 6, passed in as an environment variable
     with no default, so the script refuses to guess) and
     `DATA_SNAP="${DATA_DS}@rehearsal-sandbox"`.
   - Take the media snapshot and the data snapshot as **one atomic step**:
     `sudo -n zfs snapshot "$MEDIA_SNAP" "$DATA_SNAP"` when both datasets are
     in the same pool (one command is atomic across them). If they are in
     different pools, take them back to back and say so in the script output:
     the rehearsal reads only the store, so the store snapshot is what must
     be consistent.
   - Before the snapshot, if the caller sets `CENSUS_URL` and `CENSUS_TOKEN`
     (no defaults), fetch `GET <CENSUS_URL>/api/v1/diagnostics/db-census` from
     prod and save it beside the logs as `census-prod-<snapshot time>.json`.
     The exit check compares it with the sandbox's.
   - Delete the rsync and the pristine copy (`:37-58`). Nothing is copied.
2. **`sandbox-reset.sh`: destroy and re-clone the data too.**
   - Add `DATA_CLONE_DS` (a new dataset in the app-data dataset's pool, for
     example `<pool>/<parent>/rehearsal-sandbox-data`) and `DATA_CLONE_MNT`
     (for example `/mnt/aorg-sandbox/data`; not under `/tmp`, not under
     `/var/lib`).
   - Destroy and re-clone it with the same retry loop the media clone uses
     (`:32-48`), from `DATA_SNAP`, with `-o mountpoint="$DATA_CLONE_MNT"`.
     Keep the divergence check for both clones.
   - **Ownership.** The clone keeps the service user's ownership, which the
     user namespace cannot map (bootstrap header, `:6-10`). After the clone,
     run `sudo -n chown -R "$(id -u):$(id -g)" "$DATA_CLONE_MNT"`. This
     rewrites metadata on the clone only (copy-on-write), never on prod.
     Record its duration in the report. If the owner prefers running the
     instance as the service user instead, note it as an open item; do not
     build both.
   - Delete the `rm -rf "$WORK"; cp -a "$PRISTINE" "$WORK"` restore
     (`:55-58`).
   - Leave a commented placeholder for a second data clone (the signal-store
     dataset C8a will choose, plan C9), with the same destroy-and-re-clone
     shape. Do not create it.
3. **`sandbox-run.sh`: prod limits and a cgroup cap.**
   - Bind `$DATA_CLONE_MNT` over the prod store's parent directory (from
     anchor 6) instead of `/tmp/abk-sandbox/data` over
     `/var/lib/audiobook-organizer`, and pass `--db` as the prod store path,
     so every absolute path the app derives resolves inside the clone. Keep
     the fail-closed check that the bind took effect.
   - Set `GOMEMLIMIT` and `GOGC` to the values read from the installed prod
     unit (anchor 6), passed in as environment variables with no defaults.
   - Wrap the launch in
     `systemd-run --user --scope -p MemoryMax="$PROD_MEMORY_MAX" -- unshare ...`.
     If `systemctl --user show -p DelegateControllers` does not list
     `memory`, use
     `sudo -n systemd-run --scope --uid="$(id -u)" --gid="$(id -g)" -p MemoryMax="$PROD_MEMORY_MAX" -- unshare ...`
     and say which one the script uses. Print the scope's cgroup path, so a
     rehearsal can read its `memory.peak`.
   - Keep `SANDBOX_BIN` (the cut-over build is started through it), port
     `8485`, and the log file, moved out of `/tmp` next to the clone's mount.
   - Apply the four switches from anchor 7 for every run (flags or
     environment in the launch; a persisted setting is written on the clone
     only, after reset, never on prod).
   - The settings-encryption key lives in the constant secure state
     directory, not beside the store. Confirm the instance starts and logs
     "Settings encryption initialized"; if it refuses because it cannot read
     the key, bind the prod state directory read-only into the namespace and
     record that you did. Never let it generate a new key.
4. **`docs/runbooks/dedup-sandbox.md`.** Rewrite "Replayable sequence" for
   the clone flow and the new variables, add a short "Rehearsal use" section
   (start the cut-over build with `SANDBOX_BIN`, read `memory.peak` from the
   printed cgroup, reset = re-clone), and move "data is a copy" statements to
   "data is a clone". Bump its header.
5. **Run it.** `sandbox-bootstrap.sh`, `sandbox-reset.sh`, `isolation-test.sh`
   (must pass, unchanged), then `sandbox-run.sh` with the current prod binary.
   Wait for `memdb warmup published` in the sandbox log. Fetch the census
   from the sandbox (`https://<server>:8485/api/v1/diagnostics/db-census`,
   with a sandbox credential) and save it as `census-sandbox.json`.
6. **Compare** the two census files family by family (a short Python script
   kept in `scripts/dedup-sandbox/`, standard library only). Families
   written between the prod census and the snapshot (operation rows, logs,
   activity) may differ by those writes; every other family must match.
   Memtable contents are not in the census on either side (A2's note), so
   also compare after one `Flush` if the numbers differ in a family nobody
   wrote.

## 6. Do not touch

- Production: no write to the prod store, the prod unit, or prod settings.
  Snapshots, clones and reads only.
- `audiobook-organizer` source. If a switch from anchor 7 is missing, report
  it; do not add it here.
- The media clone's shape and the isolation test's logic.
- Any other `infra-docs` file than `scripts/dedup-sandbox/*` and
  `docs/runbooks/dedup-sandbox.md`.

## 7. Tests

- `isolation-test.sh` passes after the change, unchanged (the July gate).
- `sandbox-reset.sh` run twice in a row leaves both clones at their
  post-clone divergence (about 8K) and the second run destroys the first's
  clones without a retry failure.
- With the instance running, `cat <scope cgroup>/memory.max` equals the prod
  `MemoryMax`.
- The census comparison script exits 0 on two identical files and non-zero on
  a planted difference in a non-written family.

## 8. Verify

```bash
bash -n scripts/dedup-sandbox/*.sh
shellcheck scripts/dedup-sandbox/*.sh   # if installed; report findings
python3 -m py_compile scripts/dedup-sandbox/*.py
```

Then the run in step 5 and the comparison in step 6.

## 9. Deliverables

- Version headers bumped on every changed file (the scripts carry
  `# file:` lines; add `version`, `guid` and `last-edited` if missing).
- No private addresses and no API-key-shaped strings in any file:
  `git diff origin/main | grep -nE "172\.16\.[0-9]{1,3}\.[0-9]{1,3}|ab""k_[A-Za-z0-9]{16,}"`
  prints nothing.
- Commit, for example
  `feat(dedup-sandbox): clone the app-data dataset; prod memory limits; rehearsal-ready`,
  ending with the session's `Co-Authored-By` and `Claude-Session` lines.
- Push by explicit sha to `feat/sandbox-zfs-clone` and open a PR in
  `falkcorp/infra-docs`. Do NOT merge; the owner reviews.

## 10. Exit criteria and report

- [ ] No store copy anywhere: the data comes from a clone of a snapshot of
      the app-data dataset; reset destroys and re-clones it.
- [ ] The instance runs under a scope with the prod `MemoryMax`, and prod's
      `GOMEMLIMIT` and `GOGC`.
- [ ] Scheduler, operation resume, scans and outbound fetchers are off for
      the run (each switch named with file:line), or the missing switch is
      reported and the task stopped there.
- [ ] `isolation-test.sh` passes.
- [ ] The sandbox opens, and A2's census on the sandbox matches prod's census
      taken at snapshot time, family by family, apart from families written
      in between (listed).
- [ ] PR open in `infra-docs`, not merged.

Report:

```
TASK-A8 report
infra-docs head sha: <sha>
PR: <url>
app-data dataset: <name> (pool <pool>), zfs list -o space: <line>
switches: scheduler <file:line>, resume <file:line>, scans <file:line>, fetchers <file:line>  (or MISSING: <which>)
memory: MemoryMax <value> via <systemd-run form>; GOMEMLIMIT <v>; GOGC <v>
chown on clone: <duration>
isolation test: PASS / FAIL
census: <n> families equal; differing: <family: prod vs sandbox, reason>
not done / deviations: <list or "none">
```
