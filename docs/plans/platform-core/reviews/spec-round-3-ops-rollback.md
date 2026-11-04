<!-- file: docs/plans/platform-core/reviews/spec-round-3-ops-rollback.md -->
<!-- version: 1.0.0 -->
<!-- guid: a97b25e8-5056-4ae0-9420-e1eb4b58ceb5 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 3: ops, rollback and incremental delivery (lens OPS3)

Lens: walk S0–S10 of [09](../../../specs/platform-core/09-abo-on-core.md) v1.2.1 (S2b, UI-0, S3 r1, S4 r1
included). For each stage: can it ship, is its done-check measurable, can it roll back to its Teach build
(and does it refuse below that build), is boot time budgeted? Also covered: release gating per 14 Q-I
(merges are ungated and RCs auto-promote), the updater's P−1 `Boot` and `binary_not_writable`, the
systemd and launchd deploy files (`KillMode`, `EXTEND_TIMEOUT_USEC`), and how `min_upgrade_from` is
enforced. Code anchors are `path:line @748fd787a`. Spec anchors are `file §section` (09 line numbers are
from v1.2.1).

Counts: **1 BLOCKER, 13 MAJOR, 8 MINOR** (22 findings).

## A. Regression check (round-1 and round-2 OPS BLOCKERs and MAJORs)

| ID | Status in round 3 | Open finding |
|---|---|---|
| OPS-1 / OPS2-14 Docker build path | Fixed: full image on every non-doc PR, run and ready check. But the job is not a merge gate on most paths | OPS3-3 |
| OPS-2 / OPS2-11 skipped releases, stepping stone | Text fixed (`min_upgrade_from`, adopt steps kept). Broken: the S10 and T8 floors cannot be expressed, and the updater checks the binary version, not the store | OPS3-7 |
| OPS-5 / OPS2-12 env names in deploy files | Fixed | — |
| OPS-6 / OPS2-2…6 updater | OPS2-2, 3, 4 and 6 fixed. OPS2-5: half-fixed | OPS3-12 (manual downgrade; second auto-apply path), OPS3-17 |
| OPS-16 / OPS2-7 usage gate | Fixed in 09 §9.2.1. P8 cannot evaluate it | OPS3-18 |
| OPS-18 / OPS2-1 release gating | Text added (§9.2.1, 14 Q-I). As specified, neither layer works | OPS3-3, OPS3-4, OPS3-5 |
| OPS-20 / OPS2-13, OPS2-25 reload check, `PREV_REF` | One mechanism, one job. S9 cell b cannot pass, and `PREV_REF` can predate the floor | OPS3-11, OPS3-6 |
| OPS-21 / OPS2-16 per-stage done-checks | Fixed (§9.3 rule; one ABS stage list in §9.3, §9.10 and P7) | OPS3-19 (two checks not measurable) |
| OPS-22 / OPS2-17, OPS2-24 boot budget | 05-health use (1) fixed. The deploy script fix cannot be implemented, the budget has no defined interval, and the labels differ | OPS3-9, OPS3-10, OPS3-16 |
| OPS2-8 blanking probe | Fixed (05-config §1.12.2) | — |
| OPS2-9 `Requires` stamps | Fixed (05-storage §5S.9.1 row 13a, 05-tasks) | OPS3-20 (C5 `Adopts` list differs) |
| OPS2-10 projection constant | Fixed (`LegacyMaxFormat + 1`, §9.0 ordering rule) | — |
| OPS2-15 big-bang stages | Fixed (S2 r1–r3, S3 r1/r2, S4 r1–r3) | — |
| OPS2-26 deploy while a restore is staged | Specified; cannot be implemented over ssh | OPS3-13 |

## B. Findings

### OPS3-1 — BLOCKER — Core's state directory drops HEAD's fallback; Docker and launchd installs either fail to boot or keep the keyring in the ephemeral container layer

- **Spec:** 05-config §1.4 and the key table (`core.paths.state_dir`: "the app's compiled default … **pre-boot**:
  read from the compiled default, env and flags only"); 06 §6.5.1 P−1/P0a/P0; 05-config §1.12 (keyring
  `<state_dir>/secrets.keyring`); 05-backup §5B.3.4/§5B.3.6 (restore journal and revocation journal under
  the state directory). No spec file mentions containers or launchd for the state directory: there are
  no hits for `/data`, `VOLUME` or a state-dir env in any deploy file.
- **Evidence:** HEAD's default is a fixed system path that an unprivileged process cannot create.
  `EnsureSecureStateDir` therefore **falls back to `filepath.Dir(database_path)`** whenever that default
  is not creatable and no explicit `ABK_STATE_DIR` is set (`internal/config/state_dir.go:118-155`; the
  comment names "any container that does not pre-create it"). The image runs as `USER audiobook` and its
  only persistent path is the data volume (`Dockerfile:93-96,104`). Compose sets no state-dir variable
  (`docker-compose.yml:17-22`). The plist sets only `DATABASE_PATH` (`deploy/com.audiobook-organizer.plist:36-43`).
  On those installs, `.encryption_key` therefore lives today in the DB's directory on the volume. A core
  build that follows the spec resolves the compiled default with no fallback, which gives two outcomes:
  - Boot fails at P0 or P−1. Docker is then unshippable from the stage where core resolves `state_dir`
    (S2's loader and boot record, at the latest S3's keyring). The S0 image job would only catch it.
  - The implementer pre-creates the directory in the image. Then the keyring, the revocation journal
    and the restore intents live in the container layer. A `docker compose pull && up` recreates the
    container and loses the keyring, and with it every secret: the "silently destroys secrets" hazard
    that `state_dir.go:44-51` documents. The import also misses the existing `.encryption_key` on the
    volume.
- **Fix:**
  - In 05-config, define pre-boot resolution as: explicit env or flag; else the compiled default when it
    can be created; else `filepath.Dir(core.storage.path)` resolved from flags and env, with HEAD's WARN.
    An explicit value never falls back.
  - P−1 uses the same rule, with the marker's `db_path` as the DB input.
  - In 09 S2 "Deploy", set the canonical state-dir variable to the DB's directory in the Dockerfile
    (`ENV`), in compose and in the plist. That is exactly where HEAD's fallback put `.encryption_key`, so
    `k0-legacy` is found.
  - Extend the S0 image job: after a seeded S3 boot, assert that the keyring file is on the volume and
    that the secrets still decrypt after the container is recreated. Add `TestStateDirFallbackMatchesHEAD`.

### OPS3-2 — MAJOR — S0's phase T blanks blob fields that HEAD reads, so S0 → HEAD hides every non-legacy secret saved on S0

- **Spec:** 05-config §1.12.2 phase T ("T also **writes** them … and it blanks the field in the blob", build
  "09 S0"); 09 S0 "Rollback" (line 540: "the only keys written are `c/storage/install` and the served
  ledger"); §9.4.1 phase T row ("no (inert until S3)").
- **Evidence:** S0 is level 1 and nothing raises the store before S3 r2. HEAD is therefore a legal
  `make rollback` target from S0, and the S0 done-check itself rolls back S0 → HEAD. HEAD's loader knows
  4 secret rows and rejects every other `setting:` key (`internal/config/persistence.go:1884`
  `unknown setting key`). It reads all other secrets as plaintext from the blob, which T has blanked. A
  secret saved on S0 (an API key, a password outside the 4) is therefore empty on HEAD after rollback, and
  its feature breaks until roll-forward. The S0 rollback row and the "inert" cell are wrong, and the
  mandatory S0 R-test (§9.3 rule: write on cur, read on prev) fails as written.
- **Fix:** In 05-config phase T, write the legacy-format row **and keep the plaintext field in the blob**
  as HEAD does. Blanking happens only under X's rule (both ledger and `.prev` probe). A clear deletes the
  row and empties the field, which HEAD reads correctly. Correct the 09 S0 rollback row ("also legacy
  secret rows; HEAD keeps reading the blob"). Add the S0 R-test row "secret saved on S0 is readable on HEAD".

### OPS3-3 — MAJOR — Release-gating layer 1 misses most of the code it protects and can be bypassed by admin merges; layer 2 never protects the reference store

- **Spec:** 09 §9.2.1 "Release gating" item 1 (lines 151-157); 14 Q-I recommendation; 09 S5 (T2–T7 "suited
  to `/parallel-sweep`").
- **Evidence:**
  - **Trigger.** Some triggers are file content, not paths: "a `RaisesLevel` or adopt step, a `Mirror`
    declaration". The rest depends on a self-applied label (`compat-gate`).
  - **Missing paths.** The trigger list omits the ABO code whose invariants the gates exist for:
    - adopted-prefix writers outside `core/`: `internal/usermerge` and the admin debug edit (09 §9.11 S4
      row), which `TestAdoptedPrefixWritesGoThroughMirror` is meant to catch;
    - gin routes and the route manifest (`TestLegacyRoleReachUnchanged`, R6);
    - `keyfamilies.go` and books writers under strict claims (S2b);
    - `deploy/**` (`TestShippedDeployEnvNamesArePermanent`);
    - `web/src/services` (cells a/b);
    - the rollback tooling itself: `scripts/storage_format_guard.py`, the `rollback` target,
      `deploy-wait-ready.sh`, `scripts/ci/build_web.sh`.
  - **Admin bypass.** A required check does not stop admin merges unless admins are enforced. The
    workflow 09 recommends for S5 merges with `gh pr merge <n> --rebase --admin`
    (`.claude/skills/parallel-sweep-impl/references/coordinator-prompt.md:122`, `scripts/pr_merge.py:209-212`
    of that skill). CLAUDE.md's Quick Fix workflow merges right after `gh pr create`.
  - **Reference host.** The reference production host deploys `origin/main`, not releases
    (`scripts/deploy-preflight.sh`; `Makefile.local.example:88-110`). Layer 2 (promotion) therefore
    protects only `releases/latest` consumers, never the store that P8 and W are measured on.
- **Fix:**
  - `extraction-gates` computes its own classification from the diff. Its path list is: `core/**`,
    `go.mod`, `go.sum`, `Dockerfile*`, `deploy/**`, `docs/compat-surfaces.md`,
    `internal/database/keyfamilies.go`, `internal/usermerge/**`, route manifests and `wire_*.go`,
    `web/src/services/**`, the generated client, `scripts/storage_format_guard.py`,
    `scripts/deploy-*.sh`, `scripts/ci/build_web.sh` and the `Makefile` rollback and deploy targets. It
    also greps the diff for `RaisesLevel|Floor:|Adopts:|AdoptStep|Mirror\{`. It applies no label.
  - Run the fast Go gate tests (ratchets, ledger, reach) on every PR that touches Go. Keep
    `compat-rehearsal` for classified PRs and the nightly run.
  - Protect `extraction-gates` with a ruleset that has no bypass actors (or `enforce_admins: true`).
    Amend the parallel-sweep coordinator and CLAUDE.md's Quick Fix flow to wait for it and never use
    `--admin` past it.
  - State in §9.2.1 and 14 Q-I that layer 2 protects release consumers only.

### OPS3-4 — MAJOR — The RC promotion gate cannot be implemented as written

- **Spec:** 09 §9.2.1 item 2 (lines 158-161); S0 "Gates" row and the done-check "`check-rc-ordinal` refuses a
  red RC"; 14 Q-I option (b); §9.2.9 ledger states.
- **Evidence:**
  - **(a) Timing.** `check-rc-ordinal` is a job of `prerelease.yml` that runs right after the RC is cut
    on push (`needs: prerelease`, `.github/workflows/prerelease.yml:59-72`). CI for that commit is still
    running at that moment. "Refuses when the RC commit's CI is not green" therefore either always refuses
    (pending is not green), so production minors stop, or it treats pending as a pass and gates nothing.
  - **(b) Undefined state.** "A stage in state 'dark PRs landed, flip not landed'" is not a ledger state.
    §9.2.9 rows are per **surface**, with states `aliased | gone | retired | removed`. There is no
    per-stage record for a job to read.
  - **(c) Contradiction.** "The operator, not the RC counter, chooses the production release that carries
    a flip" contradicts the auto-promotion every 10 RCs that the same section keeps.
- **Fix:**
  - Move promotion to a `workflow_run` on CI `completed` for the RC's SHA, or to a scheduled job. It
    queries that SHA's check suite (including `extraction-gates`) and the latest nightly
    `compat-rehearsal` conclusion for a SHA at or after it.
  - Add a machine-readable stage table owned by 09, for example `docs/compat-stages.json` with, per
    sequence, `teach_merged`, `adopt_merged`, `flip_merged` and the SHA and release of each, which the
    job reads.
  - Either drop the "operator chooses" sentence, or make flip-carrying releases manual-dispatch only
    (the job refuses to auto-promote while the newest flip SHA is not yet in a production release).

### OPS3-5 — MAJOR — Blocking promotion during the dark phase puts the Teach build, the adopt step and the flip into one production release, so release consumers have no rollback target

- **Spec:** 09 §9.2.1 item 2; S4 "Releases" (line 633: r1 "production-deployed before r2"); §9.2.2 Teach
  table; 14 K19 ("The Teach build ships before every adopt step and serves production under normal deploys
  first").
- **Evidence:** The promotion block holds from the first dark PR of a stage until its flip lands. For S4,
  r1 (the Teach build), r2 (the adopt step, `Floor` 3) and r3 (the flip) all land in that interval, so the
  first production release after S4 contains all three. The same can happen to the S4 r3 Teach build and
  S5 r1, and to S6 and S7. "Production-deployed" holds only for the reference host, which deploys main.
  A Docker `:latest` user or an updater install jumps straight from a pre-Teach release to a release that
  adopts. The adopt step runs on the first boot, by design, even when an update is pending
  (05-storage §5S.3.5 `RequiresSelfServed` excludes adopt steps). The previous image or `.prev` is then
  below the floor: `make rollback` refuses, `Boot` records `rollback_blocked`, and re-pinning an older
  tag refuses. K19's mitigation does not reach these installs.
- **Fix:** Replace the dark-phase block with an **ordering rule** that the promotion job enforces from
  OPS3-4's stage table: a production release may contain an adopt step only if that sequence's Teach build
  is in an **earlier** production release. Cut every Teach build as its own minor by manual dispatch
  before the adopt PR merges. Say this in K19 and in the §9.2.2 Teach table (column "must be in a
  production release before").

### OPS3-6 — MAJOR — The R-test's default `prev` can predate the sequence's Teach build, so `compat-rehearsal` is red by design and blocks promotion

- **Spec:** 09 §9.10 R-test row ("`PREV_REF` — by default the latest production release tag"); §9.3
  "Rollback rehearsal"; §9.2.1 (a red `compat-rehearsal` blocks promotion).
- **Evidence:**
  - Releases are irregular, and with OPS3-5 the latest production tag is usually older than the Teach
    build while an adopt PR is in flight.
  - The R-test boots `cur` first. Its adopt step stamps the `Floor` (S3 r2: 1, S4 r2: 3, S5 r1: 4,
    S7: 5), so the following boot of `prev` is refused with `LevelTooNewError`. That refusal is correct
    behaviour, but the job goes red.
  - The job runs nightly and on gated PRs, and a red result blocks promotion. The release that would
    contain the Teach build, and so move `PREV_REF` past the floor, can then never be promoted: a deadlock.
- **Fix:** `PREV_REF` defaults to the newer of (latest production tag, the newest Teach build of every
  sequence whose adopt step `cur` registers, from 09's stage table). Add the explicit cell "`prev` below the
  floor: refused with `LevelTooNewError`, store byte-identical", which asserts the refusal instead of
  failing on it.

### OPS3-7 — MAJOR — The `min_upgrade_from` floors of S10 and T8 cannot be expressed, and the updater enforces them on the wrong object

- **Spec:** 09 §9.2.2 "Minimum-upgrade-from release" (lines 238-260; S10 names "the newest release that still
  carries the mirror code"; "Releases newer than the minimum may be skipped"); S10; 05-updater §5U.4 step 3 and
  §5U.5 "Stepping stones"; 05-storage §5S.3.5 `UpgradeFloor{Owner, Version}` and §5S.3.11 Contract row; 05-tasks
  §5T.8 T8 row; 02 N14 ("must pass through it").
- **Evidence:**
  - **(a) Passing through is not enough.** A contract runs on a store only after its expand level has
    served that store for 30 uninterrupted days, plus the build's own 1 h of readiness. An install that
    upgrades to the minimum release and moves on after 10 days has not run C3–C8.
  - **(b) The updater checks the binary version.** It compares the *running binary's* version with
    `min_upgrade_from`, so it applies S10. S10 then refuses the store at boot with `ErrUpgradeFloor`. The
    unit loops into `Boot` rollback, and Docker loops under `restart: unless-stopped`.
  - **(c) The floor type cannot express a contract.** `UpgradeFloor` compares an owner's applied
    **version**. Contracts are `DataMigration`s, recorded in `c/storage/dm/<id>`, and bump no version.
    The level cannot stand in either: S7's adopt `Floor` 5 already exceeds C3's 2 and C4's 4 on a store
    where neither ran.
  - **(d) T8 has the same problem.** It deletes "ABO's opv2 writers" and ships "with or after C5". That
    condition is release-level, while C5 is deferred per store, so a T8 build can serve a store still in
    tasks mirror phase without the code that phase needs.
- **Fix:**
  - `UpgradeFloor` gains `Contracts []StepRef`: refuse unless each contract's `dm` record is in its final
    phase. T8 declares `Contracts: [C5]`; S10 declares every contract whose mirror code it deletes.
  - The release manifest replaces the version-only `min_upgrade_from` with
    `requires_store: {owners: {books: 66}, contracts: [...]}` (the named install-first release is kept
    for messages).
  - Every guard-aware boot writes a sidecar `<db>.storage-applied` listing applied versions and finished
    contracts. Updater step 3 compares it with `requires_store` and refuses with `stepping_stone_required`.
  - Add a P-command that prints the sidecar before an S10 or T8 deploy.
  - Done-check: "store with C4 deferred → S10 refused before any write, and the updater refuses to apply
    S10 to it".

### OPS3-8 — MAJOR — C7 stops the integration mirror without raising the level, so S5 and S6 can still open the store and run on stale integration settings

- **Spec:** 09 §9.5 C7 row (line 780: "last M1.4 step 2a re-import, stop the blob mirror"; required level "—");
  §9.2.2 level table (S5–S7 all level 5) and Integrations Teach row (`Floor` 5); 05-metadata M1.4 step 2a
  ("At C7 the import runs one last time, the mirror stops").
- **Evidence:** C7 usually runs before C8 (its window starts one stage earlier). Between C7 and C8 the
  store is at level 5, which S5 and S6 builds serve, and those builds read Deluge, AI, SMTP and webhook
  settings only from the legacy keys that are no longer mirrored. After a rollback, S6 therefore uses
  stale values:
  - an old Deluge password;
  - a webhook URL deleted on S7 that still receives library events;
  - an old secret paired with a host whose secrets S7 cleared on a URL change (SEC-15).

  This breaks the rule every other contract follows: C3 → 2, C4 → 4, C5 → 5 each lock out the builds
  that read the retired form.
- **Fix:** Either renumber (S7 declares level 6, "integrations from the instance store without legacy
  keys"; C7 `RaisesLevel` 6; S8 → 7; C8 → 7; legacy restore bound `{core, 6}`), or, more simply, give C7 the
  store-side precondition "`core` level ≥ 6 (C8 applied)" so the mirror stops only after no pre-S8 build
  can open the store. Update 05-metadata M1.4, 05-notify §10, 05-download and the §9.5 row, and add the
  R-test row "S7 edit after C7 → rollback to S6 refused".

### OPS3-9 — MAJOR — The fixed `deploy-wait-ready.sh` cannot see `systemctl` state: it runs on the operator's machine

- **Spec:** 09 S0 "Deploy" (line 539: the script "keeps polling while `systemctl show -p ActiveState` is
  `activating`"); §9.3 (`DEPLOY_READY_TIMEOUT` bounds only the time after leaving `activating`); 05-health
  §5H.3.4.
- **Evidence:** Both recipes run the script locally against a URL: `bash scripts/deploy-wait-ready.sh
  $(DEPLOY_URL) 300` (`Makefile:916`; the `deploy` target in `Makefile.local.example`). The script takes
  only `BASE_URL [TIMEOUT]` (`scripts/deploy-wait-ready.sh:8-13`). A local `systemctl show` queries the
  wrong machine, or a macOS host, which the recipe supports for cross-compiling. So the long-start fix
  cannot work as written. A legitimate 12-minute migration still "fails" the deploy, which invites a
  `make rollback` (a restart) mid-migration.
- **Fix:**
  - Change the signature to `deploy-wait-ready.sh BASE_URL TIMEOUT [SSH_HOST UNIT]`.
  - Poll `ssh $SSH_HOST systemctl show -p ActiveState,SubState,NRestarts $UNIT`:
    - `activating` counts as progress;
    - `failed`/`inactive`, or `NRestarts` rising, fails at once (a crash loop is reported in seconds,
      not after 300 s);
    - `active` starts `DEPLOY_READY_TIMEOUT`.
  - Without `SSH_HOST` (Docker, launchd), keep URL-only polling with a documented larger timeout.
  - Add `scripts/tests/test_deploy_wait_ready.py` with a fake `ssh`.

### OPS3-10 — MAJOR — The boot-time budget measures no defined interval and cannot hold across a year of stages

- **Spec:** 09 §9.3 "Boot-time budget" (line 506: ready within "the S1 baseline + 10 %"; `DEPLOY_READY_TIMEOUT`
  bounds only post-`activating` time); S1 done-check (baseline into the ledger); "Every stage's Done when
  also includes this section's boot budget".
- **Evidence:**
  - **(a) Undefined interval.** It is not said whether the budget runs from restart or from leaving
    `activating`. Measured from `activating`, it excludes the checkpoint, adoption and contract phases,
    which are the ones that grow.
  - **(b) No first-boot / steady-state split.** The first boot of S2b, S3 r2, S4 r2, S5 r1, S7 and every
    contract takes a checkpoint and adopts or deletes in batches, so it will legitimately miss +10 %.
    Steady-state boots also add per-boot rebuilds (identity, config, integration re-import, tasks
    reconcile).
  - **(c) Library growth.** S1 → S9 spans many months of library growth. A fixed S1 baseline will exceed
    +10 % from growth alone, so the check gets waived or fails spuriously.
- **Fix:** Redefine the budget per stage as an A/B on **one** restored copy:
  - boot `prev` and `cur` 3× each and measure restart → `"ready":true`;
  - steady state: p50(cur) ≤ 1.10 × p50(prev);
  - first adopting or contract boot: report each `mediacore_kernel_boot_phase_seconds{step}`, with an
    absolute ceiling recorded in the ledger per stage (owner sets it, e.g. ≤ 2 × steady state + the
    checkpoint).

  The S1 number becomes the first data point, not the reference.

### OPS3-11 — MAJOR — The S9 rollback row and R-test cell b rely on the reload check that S9 deletes

- **Spec:** 09 S9 "Moves" ("the S0 `X-App-Build` check is deleted in favour of the shell banner") and "Rollback"
  (line 711: "the S0 build check offers a reload to tabs left open"); §9.10 cell (b) ("new tabs, S0 reload
  prompt"); 07 §7.5.2 (banner only when `app.ui_build_id` and the bundle ID are both non-empty), §7.5.1
  (`ui_build_id` is `""` when a legacy mount owns `/`, i.e. every pre-S9 server).
- **Evidence:** After S9 → S8, an open S9-shell tab talks to S8. S8's `/api/core/v1/modules` reports
  `ui_build_id: ""`, so the banner is suppressed, and the S0 check no longer exists in that bundle. No
  reload is offered, deep links to `/books/...` 404 on reload, and the mandatory S9 cell b cannot pass.
- **Fix:** In 07 §7.5.2, a non-dev bundle treats a missing or empty `ui_build_id`, **or** an `X-App-Build`
  response header that differs from its own ID, as a mismatch. The shell's API client keeps reading
  `X-App-Build`, which comes from the same source, so a pre-S9 server still prompts a reload. Correct the 09
  S9 rollback row and cell (b) to name the shell banner.

### OPS3-12 — MAJOR — The S0 updater hotfix leaves downgrade-by-inequality and no reader check on manual apply, and misses the second auto-apply path

- **Spec:** 09 S0 "Updater hotfix" (line 538: scheduled apply check-only, "it notifies … a manual apply stays
  available"); §9.4.1 updater row; §9.12 O9.
- **Evidence:**
  - **Downgrade.** "Update available" is `latestVersion != currentVersion` (`internal/updater/updater.go:153`).
    The reference host runs main builds whose `git describe` version differs from `releases/latest`, so
    the new S0 notification and banner advertise a **downgrade** every check.
  - **No guard on manual apply.** A manual apply installs it with no guard check
    (`updater.go:214-280`). Once any adopt step has run (S3 r2 `Floor` 1, S4 r2 `Floor` 3, …), the older
    binary is refused at open. It exit-loops into `StartLimitBurst`, and nothing rolls back (the HEAD
    updater has no `Boot`).
  - **Second apply path.** Scheduled apply has a second path that the hotfix text does not name: the
    maintenance-window op's step 1 calls `DownloadAndReplace` directly
    (`internal/server/scheduler_maintenance_window_op.go:72-91`).
- **Fix:** The S0 hotfix must:
  - compare with `golang.org/x/mod/semver` and refuse a target that is not newer;
  - run `<exe>.new --print-storage-levels` (falling back to `--print-storage-format`) against the
    sidecar before the swap, using the guard table S0 already ships, and refuse with the reason;
  - make **both** apply paths check-only.

  Done-check: the existing updater test also asserts refusal of an older tag, refusal below the store's
  level, and that the maintenance-window step does not apply.

### OPS3-13 — MAJOR — The pending-restore refusal in `make rollback` and `deploy` cannot read the intent over ssh

- **Spec:** 09 S1 "Deploy" (line 571) and "ABO-local fixes"; §9.2.2 "Rollback tooling" (the guard "refuses
  while a staged restore is pending"; the only new sudoers line is for `<db>.storage-levels`); 05-storage
  §5S.3.7 reader check; 05-backup §5B.3.4 step 8.
- **Evidence:** The intent is `<state_dir>/restore/pending.json`, and the state directory is 0700 and
  service-owned (`internal/config/state_dir.go:112-116,135`). Both recipes run as the ssh user over a
  non-tty ssh with `sudo -n` and literal sudoers paths
  (`docs/system/deploy-and-gpu-ops.md:195-218`). There is no state-dir variable (only `DEPLOY_BIN` and
  `DEPLOY_DB`, `Makefile:893-895`) and no sudoers line for a test inside the state directory. The spec
  does not say whether "cannot tell" refuses every rollback or silently passes. The first breaks
  rollback from S1 on; the second re-opens OPS2-26.
- **Fix:**
  - Add a `DEPLOY_STATE_DIR` variable.
  - S1 owner action: add the literal sudoers line `/usr/bin/test -e <state_dir>/restore/pending.json`.
  - The guard script treats "cannot determine" as **refuse**, with a message naming the missing sudoers
    line, and an override `ROLLBACK_IGNORE_RESTORE=1`.
  - Add cases to `scripts/test_storage_format_guard.py`.
  - The `deploy` recipe uses the same probe.

### OPS3-14 — MAJOR — The checkpoint of a deferred contract is reused for weeks, so the documented recovery discards the writes made since

- **Spec:** 05-storage §5S.3.12 ("When": a checkpoint is taken if "a level step" is pending; "Crash loops":
  reuse of the oldest unconsumed ancestor; "Consumed": `completed_at` written only "when nothing is
  deferred", §5S.4 step 9); 09 §9.2.2 contract rule (deferred ≥ 30 days); §9.8 "checkpoint kept unconsumed
  for the offline restore".
- **Evidence:**
  - **Weeks-old checkpoint.** A contract build's first boot finds the contract pending but deferred, and
    takes a checkpoint. Every later boot during the ≥ 30-day deferral has the same from-vector, so it
    **reuses** that checkpoint. `completed_at` is never written while the step is deferred, so the
    checkpoint is never consumed.
  - **Data loss on recovery.** When the contract finally runs and fails, the operator's documented
    recovery (`storage restore-checkpoint`) rolls the store back by weeks, losing every library write
    since.
  - **Disk.** Meanwhile the hard-linked checkpoint diverges toward a full copy of the store, beyond the
    one-time free-space check.
- **Fix:**
  - A deferred step is not "pending" for §5S.3.12. Evaluate `RequiresServed`/`RequiresSelfServed`
    read-only before step 6, and take a checkpoint only when a step will actually run.
  - Reuse an existing checkpoint only for a real crash loop: no boot reached readiness since `taken_at`,
    per the self-served record. Otherwise take a fresh one.
  - Add `TestDeferredContractTakesFreshCheckpoint`.

### OPS3-15 — MINOR — The S8 owner action for the service-owned binary directory is incomplete

- **Spec:** 09 S8 "Preconditions" (owner action: "install the binary into a service-owned directory referenced
  by `ExecStart`"); 05-updater §5U.5 "Supported install layouts".
- **Evidence:** The committed unit still has `ExecStart=` pointing at the system binary directory
  (`deploy/audiobook-organizer.service:92`). The sudoers `cp` lines name that path literally
  (`docs/system/deploy-and-gpu-ops.md:216-217`). The existing `.prev` stays in the old directory, so the
  first `make rollback` after the move finds no `.prev`. Before C3, S3's blanking probe then sees no
  `.prev` and reverts the blob to plaintext with `secrets_plaintext_mirror`.
- **Fix:** Turn the S8 owner action into a checklist:
  - S8 commits the unit with layout (a) as default;
  - update the drop-in `ExecStart`, the sudoers template, `DEPLOY_BIN` and `deploy-match-unit-type.sh`;
  - copy both the binary and `.prev` to the new directory;
  - verify with a dry-run `make rollback` and the updater check's `binary_not_writable` clearing.

### OPS3-16 — MINOR — The boot-phase metric still has two label sets (OPS2-24 half-fixed)

- **Spec:** 06 §6.9 (`phase` = `P-1`, `P0a`, `P0`…`P8` "in ASCII", P4 sub-steps only via `step`) vs 09 §9.3 and
  §9.10 (`P−1` with U+2212, and `P4a`–`P4d` as phase values); 09 S5 done-check ("boot phase `P4d` step
  `core/tasks#1`").
- **Fix:** One label set in 06 (`P-1`, `P0a`, `P0`, `P1`…`P3`, `P4a`…`P4d`, `P5`…`P8`, ASCII). 09 refers to
  06 instead of restating it, and the S5 check uses those exact values.

### OPS3-17 — MINOR — 06 says a HEAD updater check runs in P−1 before S8; S0 ships none

- **Spec:** 06 §6.5.1 "Stages" (line 985: "until S8, P−1 is the hotfixed HEAD updater's own check (09 S0)") vs 09
  S0 updater row (no boot counter or rollback; that is only O9's alternative).
- **Fix:** "Until S8, P−1 is absent: the hotfixed HEAD updater has no boot check, unless the owner picks 09
  §9.12 O9's boot-counter alternative".

### OPS3-18 — MINOR — P8 cannot evaluate the boot-read gate (OPS2-7 half-fixed)

- **Spec:** 09 §9.2.1 (boot-read rule: "not set on any boot within the last 14 days, **and** at least one
  recording boot in that period"; recording-build continuity); P8 command (line 838 filters only on
  `last_used_at`); §9.2.9 `Usage.LastSeenSetBoot` (a boot **ID**, not a time).
- **Evidence:** Suppose a unit sets a window alias and has not restarted for 20 days. Its `last_used_at` is
  20 days old, so P8 prints nothing. Branch A reads that as "contract may run", which is exactly OPS2-7(b).
- **Fix:** `GET /api/core/v1/compat/usage` returns server-computed `gate_met`, `gate_reason`,
  `last_recording_boot_at` and `recording_continuous_since`. P8 selects `gate_met == false`, and
  `TestNoContractBeforeSunset`'s runtime twin reads the same field.

### OPS3-19 — MINOR — Two done-checks need a client-origin dimension that the usage record does not have

- **Spec:** 09 S5 done-check ("persisted usage shows no UI-originated enqueue on `task_kind` beyond the Activity
  page's aliases"); S9 done-check ("persisted usage on `sse` limited to non-UI clients"); §9.2.9 `Use(entry)` /
  `Usage{Surface, Legacy, Count, …}`.
- **Fix:** Either add a bounded `Origin` (`spa` when the request carries the SPA build-ID header with a
  cookie session, `api_key`, `other`) to `Handle.Use` and `Usage`, or restate both checks as e2e
  assertions (Playwright run with a request log).

### OPS3-20 — MINOR — 09 and 05-tasks list different `Adopts` for C5; an unfinished online copy would then run inside the boot

- **Spec:** 09 §9.5 C5 ("`core/tasks#1` applied") vs 05-tasks §5T.6 (C5 "lists the adopt step and
  `core.tasks.req-migrate` in `Adopts`"). `req-migrate` is an `Online` library-scale migration (05-tasks
  line 978). 05-storage "Skipped releases": a registered adopt step that has not run is **run first** by the
  DAG.
- **Evidence:** If `req-migrate` has not finished, C5's boot would run a library-scale copy BootBlocking.
  That costs boot budget and is the single-core incident class from CLAUDE.md.
- **Fix:** 09 lists both steps. A contract whose `Adopts` names an `Online` migration that has not finished
  is **deferred** (`storage_contract_deferred`, reason `adopt_online_pending`), never run inline.

### OPS3-21 — MINOR — "The Docker entrypoint prints the same message (05-updater)" points at nothing

- **Spec:** 09 §9.2.2 line 254. 05-updater has no Docker text and refuses to run in containers. The image has
  no entrypoint script (`ENTRYPOINT ["/app/audiobook-organizer"]`, `Dockerfile:103`).
- **Evidence:** Under compose `restart: unless-stopped` (`docker-compose.yml:23`), a refused store
  crash-loops, and the message appears only in `docker logs`.
- **Fix:** Say that the binary's `ErrUpgradeFloor` text on stderr is the Docker message. Make it exit with a
  distinct code (e.g. 78) and print `install <release> first` as its last line. The release notes of each
  `min_upgrade_from` release name the floor. Delete the 05-updater reference.

### OPS3-22 — MINOR — The Integrations Teach row names S6, but its floor admits S5

- **Spec:** 09 §9.2.2 Teach table, Integrations row (line 201: Teach build "the S6 build (any level-5 build, S5
  or S6 …)", `Floor` 5); R2-1 ("the Teach build is the minimum rollback target").
- **Fix:** Name the floor's real lower bound ("S5 r1, the first level-5 build; S6 is the build normally
  rolled back to"). Alternatively, if S6 must be the floor, the integrations `Floor` needs a level that only
  S6+ serves. Under OPS3-8's renumbering option, check that the row stays consistent.
