<!-- file: docs/system/r2-cutover-runbook.md -->
<!-- version: 0.1.0 -->
<!-- guid: f41a3172-cd47-4776-9adf-e478758564c7 -->
<!-- last-edited: 2026-11-20 -->

> **DRAFT (R2-01b).** The owner rulings O-1 to O-5 are recorded below; the known gaps are still open, and the closing PR (R2-01c) fills the
> `<...>` placeholders. Do not run this on a production host until those are filled.

# R2 Cutover Runbook

This is the owner's procedure for taking an install to R2, the one-way cutover to the core platform, and for keeping
or undoing it. It uses placeholders only: `<server>` is the production host, `<abo-url>` is the service address
(for example `https://192.0.2.10:8484`), `<archive>` is an archive file, `<RC tag>` is the release candidate you
deploy. Privileged steps go through `make` targets that call the root-owned helpers; this runbook never asks you
to type a raw `sudo` command.

**Fixing forward is the default.** Going back after R2 has run discards writes and revives revoked credentials
(see [Keep or roll back](#keep-or-roll-back)).

## Which path are you on?

| You are on | Use |
|---|---|
| An install already migrated flip by flip: each flip's own deploy ran R2's earlier steps, and it runs a recent `main` build | [Path A](#path-a-an-install-already-migrated-flip-by-flip) |
| Any other install, coming from the pre-freeze release (the tag in `ci/prefreeze_release.txt`) | [Path B](#path-b-an-install-coming-from-the-pre-freeze-release) |

Both paths end at [Keep or roll back](#keep-or-roll-back). The step numbers are the same on both paths: Step 0 to
Step 8.

## Known gaps (read before the final day)

1. **Keep from an RC tag has a gap today.** The Keep step dispatches `release-prod.yml` from the RC tag, but the
   release workflow's changelog job runs in the `ci-bot` environment, whose deployment policy admits only the
   branch `main`. From a tag ref the release and tag are created, the run goes red, and `CHANGELOG.md` is not
   collected. No release-prod run has ever been dispatched from a tag.
   **Owner ruling (O-3):** Keep runs from the RC tag through a new `changelog-collect` on/off input on `release-prod.yml`, and you dispatch it with collect turned off. A `scriv collect` PR lands on `main` afterwards to fold the changelog fragments in. The input lands in R2-01k; until then the gap above stands.
2. **No tracked make target deploys a tagged RC yet.** The owner's current deploy builds locally, which this runbook
   forbids for R2. A tracked `make deploy-release RELEASE=<RC tag>` lands in R2-01t (it stacks on the rollback
   work); until then, [Step 4](#step-4-deploy-a-tagged-rc) cannot be done from a tracked target.
3. **The archive-taking command is not pinned here.** [Step 1](#step-1-archive-off-host) says what an archive must
   contain; the exact command is unverified at the time of writing and is filled in by R2-01c.

## Path A: an install already migrated flip by flip

On this path the earlier flips' deploys already did Steps 0, 3, 5, 6, 7 and 8, each when the flip that needed it
shipped. On the final day, you only record them, and do Steps 1, 2 and 4.

| Step | Spent at | What is left now |
|---|---|---|
| Step 0, merges and identity rows | F4-08 deploy (the boot refuses otherwise) | Record the count from that deploy. |
| Step 1, off-host archive | each flip (rehearsal) | **Do it again now.** |
| Step 2, dry run | each flip's copy rehearsal | **Do it now**; see ruling O-1 (0 pending, 0 quarantined). |
| Step 3, env renames | F3-07 deploy | Record. |
| Step 4, deploy a tagged RC | none | **Do it now.** |
| Step 5, webhook receivers | F7-09 deploy | Record. |
| Step 6, rules and dashboards | F7-09 and F8-04a deploys | Record; reload only if R2 changed them since. |
| Step 7, keyring export | after the F3-07 first boot | Record; export again only after a `secrets rotate-derive`. |
| Step 8, re-send invites | F4-08 deploy | Record. |

**Owner ruling (O-1):** keep the mandatory dry run; it takes minutes. On an already-migrated store (Path A) it passes when it reports 0 pending steps and 0 quarantined rows. Anything else: stop and look.

**Owner ruling (O-2):** there is no second M8 rewrite. Store files keep the residue from rows retired by the later steps (M3 to M6), and the R2 release note says so (05-storage §5S.3.12 and §5S.3.13).

Then run, in order: Step 1, Step 2, Step 4, the [smoke run](#smoke-run), and [Keep or roll back](#keep-or-roll-back).

Path A rollback: a rollback that only swaps the binary back to the previous `main` deploy works while no
store level step is pending (`make rollback`). If a level step ran, use `make rollback-restore` to the newest
checkpoint still inside its 7 days; an older checkpoint is gone (see [Keep or roll back](#keep-or-roll-back)).

## Path B: an install coming from the pre-freeze release

Do every step, in order. This is the whole cutover on one first boot.

### Step 0: merges and identity rows

On the pre-freeze release, finish or undo every account merge in progress. A merge in progress or an identity row that
cannot be converted refuses the upgrade. Find blocking rows with the dry run in Step 2, then fix or delete each:

```bash
audiobook-organizer storage cutover-blockers list
audiobook-organizer storage cutover-blockers fix <key> ...
audiobook-organizer storage cutover-blockers delete <key>
```

Run `audiobook-organizer storage cutover-blockers --help` for the exact arguments. `list --out <file>` also writes
credential hashes to a file that only its owner can read: delete that file after the fix.

### Step 1: archive, off host

Take an archive, verify it, and copy it off the host together with `.encryption_key` and `<db parent>/config.yaml`.
Without the key and the config an archive cannot be restored. Also confirm free space (the first boot takes a
checkpoint and rewrites ranges): free space under the database's parent directory must exceed the database size, or
the boot halts before writing. Free space first if not.

The command that takes the archive: lands in R2-01c (see known gap 3).

### Step 2: dry run (mandatory)

With the R2 binary, on the archive, on the production host or any linux/amd64 machine:

```bash
audiobook-organizer storage migrate --dry-run --from-archive <archive>
```

It reports per-step counts, quarantined rows and durations, and writes nothing to the live store. If it fails, reports
a blocking identity row, or quarantines a row you cannot explain, do not go on: fix it (Step 0), run it again, or stop
and record why.

### Step 3: env renames

Rename the environment names your host sets in its own unit drop-ins, compose `.env` or plist, using the rename
table in the R2 release note (`scripts/release/assemble_r2_notes.py` writes it: lands in R2-01a). A still-set retired
protected or secret name refuses the boot and names its successor. On this host class edit `deploy/local.conf` and
deploy; never edit a root-owned unit by hand.

### Step 4: deploy a tagged RC

Deploy the newest `v<X.Y.Z>-rc.<N>` prerelease whose commit contains every R2 PR, this runbook's closing PR
included. Write the tag down: Keep cuts the release from it. Never deploy a local build or an untagged commit.

```bash
make deploy-release RELEASE=<RC tag>   # lands in R2-01t (known gap 2); no tracked target exists yet
```

- Set `DEPLOY_READY_TIMEOUT` (default 300 in the Makefile) to `<measured s>`, and the Docker `start_period`
  likewise. The measured first-boot time comes from the newest green `production-shaped-boot-time` artifact and the
  prod-copy rehearsal: filled in by R2-01c. A real first boot is much longer than the CI number on a store with a
  full rewrite pending.
- Check the version: `audiobook-organizer --version` must print the RC tag's version (`v<X.Y.Z>-rc.<N>`). Any other
  string means a different binary is running: stop.
- The release that Keep creates rebuilds binaries at the RC's commit. It is the same commit as the RC but not the same
  bytes as the binary you smoked.

### Step 5: webhook receivers

Switch each receiver to core's format: the `X-Mediacore-Delivery` header and the timestamped
`X-Mediacore-Signature`. The OIDC redirect URI is unchanged.

### Step 6: rules and dashboards

Reload the Prometheus rules and Grafana dashboards that R2 ships. The scrape key needs the `core:metrics.read`
scope.

### Step 7: keyring export

After the first boot, write the keyring to the off-host location with your backups:

```bash
audiobook-organizer secrets export-keyring --out <file>
```

Health warns `secrets_keyring_not_exported` until you do. The file is written mode 0600.

### Step 8: re-send invites

Every open invite link stops working at R2. Re-send each open invite.

## Smoke run

**Owner ruling (O-4):** the smoke run is a one-hour checklist of the six checks below, run on the RC. Run a full-day soak only if the RC carries code the production host never ran.

Six checks:

1. Sign in to the web app.
2. An ABS phone app keeps its session and syncs progress.
3. A library scan and a scheduled task each run once, with no catch-up storm.
4. A webhook reaches its receiver.
5. The Prometheus scrape answers and the alerts evaluate.
6. Health shows no quarantined row.

## Keep or roll back

### Keep

Cut the R2 production release from the RC tag you deployed, never from `main`:

```bash
gh workflow run release-prod.yml --ref <RC tag>
```

This is blocked by known gap 1 until R2-01k lands (ruling O-3); dispatch it with `changelog-collect` off.

Then, in order: record the production tag the run creates (it is R3's `UpgradeFloor.InstallFirst`, and names the
same commit as the RC tag); prepend the R2 release note to the generated notes, never replacing them; take a new
off-host archive; and end the freeze:

```bash
gh variable set RELEASE_FREEZE --body ''
gh variable get RELEASE_FREEZE
```

The second command prints an empty value when the freeze is cleared. Nothing merged after the RC is in the release.

**Owner ruling (O-5):** the updater release asset is goreleaser's raw asset name, as returned by `internal/updater.FetchOptions()`. This lands in F8-04b, with the 05-updater §5U.12 edit.

### Roll back

`make rollback-restore` restores the pre-upgrade checkpoint with the current binary, then reinstalls `.prev` and
starts. Its branches:

- **Checkpoint** (default): `make rollback-restore`. The checkpoint is deleted 7 days after it stops being needed;
  `storage prune-checkpoints` deletes it earlier.
- **Archive** (the checkpoint is gone): `make rollback-restore ROLLBACK_ARCHIVE=<file name>`, a file name inside the
  helpers' archive directory, not a path. It discards the writes made since the archive.
- **After an R2 hotfix**: `make rollback-restore RELEASE=<tag>`, where the tag is the one in `ci/prefreeze_release.txt`
  at the R2 commit you deployed. That equals the checkpoint record's `from_release`, and is the tag the recipe names
  when it refuses.
- The recipe refuses while a swap is in flight. After a failed checkpoint boot that leaves
  `<state dir>/restore/<id>/inflight.json`, stop the service and run the previous binary's
  `storage restore-offline --checkpoint <name>`, then start the service.

**What a restore costs:** every write since the upgrade (imports, edits, phone progress) and every credential
revoked, user disabled or password changed on R2 comes back. After a rollback, repeat those revocations. This is why
fixing forward is the default.

## Freeze hotfix (Path B and the R2 release itself)

A production defect found between the freeze starting and the R2 release is fixed by default in a release cut from the
pre-freeze release plus the fix, never from the R1 tag. Branch from the tag in `ci/prefreeze_release.txt`, apply the
fix, cut the production release from that branch, and deploy it as usual. Then one agent PR on `main` replaces the tag
line of `ci/prefreeze_release.txt` with the hotfix tag and re-runs the restore drill
(`TestRollbackRestoreBootsPreviousBuild`) and `TestCutoverRefusedByOlderBuilds` against the hotfix binary. The R2
build then embeds the hotfix tag as the checkpoint record's `from_release`, and that tag, not the original, is what
`make rollback-restore RELEASE=` names. On a host that already runs mid-R2 `main` (Path A), a defect is fixed
forward on `main` instead.

## Record

One tracking-issue comment, with no hosts, paths, secrets or values: the date; the path used; Step 0 counts; the dry
run result; env names renamed; webhooks switched; keyring exported; invites re-sent; smoke done or skipped; the RC tag
and that the version string matched; kept (production tag, cut from the RC tag) or rolled back (reason).
