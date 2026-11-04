<!-- file: docs/plans/platform-core/reviews/spec-round-9-ops-rollback.md -->
<!-- version: 1.0.0 -->
<!-- guid: e4225861-544d-411d-ab23-568c4870b486 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 9 — ops, rollback and incremental delivery

Lens: OPS, ROLLBACK AND INCREMENTAL DELIVERY. ID prefix `OPS9`. Spec under review: 09 v1.9.1, with 05-storage,
05-backup, 05-updater, 05-authn, 06 and 14 cross-checked. Code anchors are at `748fd787a`. Line numbers for 09 are
for v1.9.1.

**Totals: 18 findings — 0 BLOCKER, 7 MAJOR, 11 MINOR.**

I walked S0–S10 asking three things of each stage: can it ship, can its done-checks be measured, and does §9.2.1
list its rollback cost. No stage is left undeployable. Every stage still rolls back by a binary swap, and the
9-row table covers most of the rollback costs. The gaps are of four kinds:

- the soak gate can be bypassed or cannot be measured (OPS9-1, OPS9-2, OPS9-3);
- the deletions removed two rollback or restore costs together with their release notes (OPS9-4, OPS9-5);
- A-12 left done-checks with no environment to run in (OPS9-6);
- the staging procedure has a binary it cannot identify (OPS9-8) and contradicts 05-storage about pre-S0
  archives (OPS9-9).

## A. Regression check

### Round-8 OPS findings

| ID | Round-8 issue | Status in round 9 |
|---|---|---|
| OPS8-1 (MAJOR) | `operations_*` aliases collide at S5 | **Moot** (A-5: no renames, no aliases). |
| OPS8-2 (MAJOR) | Staging copy cannot be scrubbed before its first boot | **Fixed** by R8-3 (`staging-restore` at S0, scrub before boot, `test_staging_scrub.py`). Two residues remain: OPS9-8 (which binary) and OPS9-9 (pre-S0 archives). |
| OPS8-3 (MAJOR) | S8 updater done-check cannot exercise the shipped binary | **Moot** (A-6 defers the real-RC check). There is a wiring gap in its place: OPS9-17. |
| OPS8-4 (MAJOR) | 38-definition staging fire check | **Fixed**: `TestScheduleParity_AllDefinitions`, plus a ≤ 24 h smoke check (09:1094; 05-tasks:1693). |
| OPS8-5 (MAJOR) | Post-reset hook stuck `reset_inflight` | **Fixed**: the hook always records `hook_done`; `TestPostResetHook_RemoveErrorsStillDone` (09:146, 09:1031). |
| OPS8-6 (MAJOR) | Reset rollback cost dated S3 | **Fixed**: row 1 is dated "S2b (factory and store reset), S3". With `check_rollback_rows.py` deleted by A-13, nothing checks the overview anymore, and one omission has already slipped in (OPS9-15). |
| OPS8-7 … OPS8-14 (MINOR) | — | Fixed or moot: the override covers pending intents only (09:358-361); the in-process updater probe (09:361-365); exit 75 for staged restarts (S1 done-check); dated scrub cases (09:917-927); OPS8-9, OPS8-13 and OPS8-14 are moot under A-13, A-3 and A-2. Deleting OPS8-14's row 20 also deleted its release note (OPS9-5). |

### Advisor findings in this lens

| ID | Status in round 9 |
|---|---|
| ADV-5 / A-9 (soaks) | **Half-fixed.** The ledger, the gate and the owner checkpoint exist. The gate sees only the PR that changes the marker (OPS9-1). A row is not bound to the code it soaked and has no pass criteria (OPS9-2). The gate blocks reverts (OPS9-3). |
| ADV-6 / A-10 (scrub interlock) | **Fixed** (09:876-885; 05-storage:1026; tests). |
| ADV-7 / A-11 (feature work) | **Landed.** Gaps in the CI budget remedy (OPS9-13) and in where the window announcements live (OPS9-12). |
| ADV-10 (S0 user-visible changes) | **Landed** (09:943). Two problems remain: the early-fix-5 note gives no recovery (OPS9-7), and S1 and S4 r2 change visible restore behaviour with no note (OPS9-4, OPS9-5). |
| ADV-11 (generated counts) | **Landed** (09:831-835). One residue: the literal 66 (OPS9-16). |
| ADV-S5 / A-6 (updater moved) | **Landed** (09:1133; 05-updater:20-31, :509-525). Wiring and state gaps: OPS9-17. |
| ADV-S6 / A-12 (smaller staging) | **Landed**, but done-checks were left with no environment (OPS9-6). |
| ADV-S7 / A-13 (rehearsal on flip PRs) | **Landed** (09:221). Trigger gap for later releases of multi-release stages: OPS9-10. |

## B. Findings

| ID | Sev | Title |
|---|---|---|
| OPS9-1 | MAJOR | The soak gate checks only the PR that changes a marker, so the content of the next release can merge and auto-promote before the soak |
| OPS9-2 | MAJOR | A soak row is not bound to the code it soaked, and its evidence check has no pass criteria |
| OPS9-3 | MAJOR | `check_soak.py` refuses a revert of a flip PR, so a bad flip cannot be backed out of `main` |
| OPS9-4 | MAJOR | S1's staged restore has rollback consequences and a user-visible change that no §9.2.1 row or release note carries |
| OPS9-5 | MAJOR | From S4 r2 a restore disables every API key and deletes every session; row 20's deletion removed the only release note, and 09:156-158 now says such a restore costs nothing |
| OPS9-6 | MAJOR | A-12 left done-checks with no environment: four need systemd running the committed unit, and one needs a network-reachable ABS client |
| OPS9-7 | MINOR | Early fix 5 locks out an install that has the Basic gate on and an empty saved password; the S0 note gives no recovery |
| OPS9-8 | MAJOR | "The binary of the release that wrote the archive" cannot be identified, and nothing enforces it |
| OPS9-9 | MINOR | 09 says a pre-S0 archive is staged with `cur`; 05-storage and 05-backup say `staging-restore` refuses it |
| OPS9-10 | MINOR | `compat-rehearsal` never runs on S2b r2, S5 r2–r7 or the floor release |
| OPS9-11 | MINOR | S2's `TimeoutStopSec=30` applies to older builds after a rollback; HEAD's 30 s drain plus store close can exceed it |
| OPS9-12 | MINOR | Ops records go into the embedded compat ledger, so every one of them becomes a code change |
| OPS9-13 | MINOR | The 35-minute CI budget has no slack, its remedy conflicts with required checks, and its p50 is measured only once |
| OPS9-14 | MINOR | The release-ID grammar is not closed (spelling, "…", bootstrap case) |
| OPS9-15 | MINOR | Stale cross-references: the S4 overview omits row 3, and the owner-checkpoint list names S8 instead of S1 |
| OPS9-16 | MINOR | ADV-11 residue: `TestUpgradeFloor_RefusedBelowBooks66` and P13 "books ≥ 66" |
| OPS9-17 | MINOR | S8 updater move: ABO's source wiring is untested, row 2 mislabels `core.updater.*`, and S8's new state is unlisted |
| OPS9-18 | MINOR | `prerelease.yml`: bookkeeping-only PRs mint RCs, and S0's added Docker build is redundant |

### OPS9-1 — MAJOR — The soak gate checks only the PR that changes a marker, so the content of the next release can merge and auto-promote before the soak

- **Spec.**
  - 09 §9.2.1:190-206. `check_soak.py` refuses only "a PR that changes `release:` or `shipped_stage:`".
  - 09:172-177. S2b and S6–S8 "land live PR by PR". Their other state-writing PRs "merge separately … and can reach
    production through the 10-RC auto-promotion".
  - 09:1086. T2–T7 are "mechanically similar, suited to `/parallel-sweep`".
  - 14:90 claims that the gate stops "the next flip or multi-release step" from merging before the soak.
- **Evidence.**
  - A-4 deleted every tool that maps a PR to a stage. So nothing ties a live PR of release N+1 (an S6 `c/outbox/` PR,
    a T3 batch PR from a parallel sweep) to the `release:` line.
  - Such a PR can merge while `release:` still names N. It then ships in the next auto-promoted minor (dispatched
    from `main` HEAD, `.github/workflows/prerelease.yml:124-131` @748fd787a) before N's 14 days are over.
  - The gate fires only when the PR that changes the marker comes along later. By then the hazard ADV-5 describes
    has already happened.
  - The same gap holds for every PR of a multi-release stage after the PR that opened its release.
- **Fix.** Add a plain declaration that the gate checks. This is not stage-prediction machinery.
  - **09 §9.2.1 "Soaks":** every PR that changes `core/**`, `internal/books/**` or a package listed in the current
    stage's 04 rows carries a `Stage-Release: <id>` trailer, filled from the PR template.
  - `check_soak.py` refuses such a PR when `<id>` is later than the base's `release:`, unless the same PR changes
    `release:` to `<id>`. That PR then needs the predecessor's row as today.
  - A missing trailer on such a path fails, with "feature" as the explicit opt-out for feature PRs.
  - Add cases to `test_check_soak.py`.
  - Restate 14:90 to match.

### OPS9-2 — MAJOR — A soak row is not bound to the code it soaked, and its evidence check has no pass criteria

- **Spec.**
  - 09:196-205. The row format is `<release> <tag> <deploy time, UTC> <evidence SHA-256>`.
  - `soak_record.py` "reads the running binary's `--version-json`, the service's start time and restart count", and
    prints the row "only when the evidence check passes".
  - The only stated criteria are that S2b-r1's claim-violation record is empty and S4r1's census lists no
    undecodable row.
  - A-9 says "the tag".
- **Evidence.**
  - **No tag exists for the reference host.** It runs untagged `main` builds. 09 P10 (09:1274) says ".prev is
    usually not a tag" because "deploys build main". The deploy recipe builds the operator's tree
    (`Makefile.local.example:88-90` @748fd787a).
  - **"Deploy time" is undefined.** If it is the service's start time, every redeploy inside a release resets the
    14 days. The owner deploys `main` often, and production minors come every 10 RCs. If it is something else,
    nothing records it.
  - **Nothing checks that the running binary belongs to `<release>`.** After a `make rollback` to `.prev`, or
    before the release was ever deployed, the command can still certify `<release>`.
  - **`check_soak.py` can only compare a date.**
  - **S2b-r2's "zero refusals" is not in the evidence list.** 9.4.1:1181 requires it, and 05-storage §5S.9.4
    persists strict violations too.
  - **The other releases have no pass criteria.** Health always carries `auto_update_paused` wherever automatic
    update was enabled (S0).
- **Fix.** In 09 §9.2.1 and `soak_record.py`:
  - The row becomes `<release> <commit> <first deploy UTC> <evidence SHA-256>`.
  - `soak_record.py` refuses a `commit` that is `""` or `+dirty`. It reads `git show <commit>:ci/stage.txt` and
    requires `release: <release>`.
  - It keeps `<state_dir>/soak/<release>.start`, written the first time it sees a binary of `<release>`. It refuses
    when any boot since then ran a binary of another release. It can find those boots in the journal of unit
    starts and the served ledger's `build`.
  - It requires 14 days since `.start`.
  - **Pass criteria, for every release:** every owner at level 0; no health token outside an allow-list
    (`auto_update_paused`); no unit exit other than 0 or 75 since `.start`. Per release: S2b-r1 and S2b-r2 each
    need an empty claim-violation record in their mode; S4r1 needs the census.
  - `check_soak.py` verifies that `commit` is an ancestor of the PR base and that its `ci/stage.txt` names the
    predecessor.
  - Update A-9's wording in 14 K20.

### OPS9-3 — MAJOR — `check_soak.py` refuses a revert of a flip PR, so a bad flip cannot be backed out of `main`

- **Spec.**
  - 09:205-206. The gate "also refuses an unknown or **backwards** value".
  - 14 Q-I recommends option (a): required checks with no bypass actors.
- **Evidence.**
  - Suppose an S4r2 flip misbehaves on the reference host. `make rollback` fixes the host, but `main` still
    carries the flip.
  - The revert PR moves `shipped_stage:` and `release:` backwards, so the required check refuses it. Under (a)
    nobody can bypass that.
  - Meanwhile every 10 RCs `prerelease.yml` keeps promoting `main`, flip included, to `:latest`, Docker users and
    manual-apply users. The only way out is a fix-forward.
  - Under option (b), `auto-revert.yml` pushes straight to `main` (`.github/workflows/auto-revert.yml:47-50`
    @748fd787a, `contents: write`) and is not subject to the gate. So the two options behave in opposite ways.
- **Fix.**
  - `check_soak.py` admits a backwards change only when the PR is a pure revert: its diff is the exact inverse of
    the commit that advanced the line, and its message is git's "This reverts commit <sha>". It needs no soak row.
    Add a `test_check_soak.py` case.
  - 09 §9.8 gets a row for it.
  - Re-landing the flip later needs the predecessor's row as usual.
  - State that a reverted flip's release must soak afresh. Its old row, if any, no longer counts, because the
    code is re-landed.

### OPS9-4 — MAJOR — S1's staged restore has rollback consequences and a user-visible change that no §9.2.1 row or release note carries

- **Spec.**
  - §9.2.1:139-142. The table is "the one complete list of what a rollback and the roll-forward after it cost or
    change". The R-test "asserts exactly these rows".
  - S1 (09:997, Rollback row): `make rollback` refuses while a restore is pending.
  - 9.4.1: "pending or in-flight restores refused by rollback and deploy".
  - §9.2.2:358-361. `ROLLBACK_IGNORE_RESTORE=1` passes a pending intent, which "is rejected later when the open
    stamp moved".
  - R-test cell (e) (09:1372) asserts "a staged intent across a rollback".
- **Evidence.**
  - Row 1 covers only resets (from S2b). For restores, from S1:
    - a rollback is refused while a restore is staged or in flight;
    - a rollback with the override silently discards the admin's staged restore, which the roll-forward boot
      rejects;
    - a restore now waits for a restart, because the process exits 75 (S1 done-check).
  - On a plain `docker run` with no restart policy, the container stays stopped. HEAD's restore extracts live and
    never stops the process (`internal/backup/backup.go:777-870` @748fd787a).
  - S1 has no "User-visible changes" row, so no release note ships any of this. The R-test asserts a row that is
    not in the table.
- **Fix.**
  - Add §9.2.1 row 10, From S1: "While a restore is staged or in flight, `make rollback`, `deploy` and the
    updater refuse. `ROLLBACK_IGNORE_RESTORE=1` discards a staged restore, which is not applied after the
    upgrade."
  - Release-note text: "A restore now takes effect at the next restart, which the service performs itself. A
    container started without a restart policy must be started again. Rolling back while a restore is staged
    cancels it."
  - Cite row 10 in the S1 overview and Rollback rows. The R-test asserts it.

### OPS9-5 — MAJOR — From S4 r2 a restore disables every API key and deletes every session; row 20's deletion removed the only release note, and 09:156-158 now says such a restore costs nothing

- **Spec.**
  - 09:156-158 says restoring "an archive made on any release of the sequence with any other release that can open
    it" is "not a cost".
  - 05-backup:40-44, B7 (:86), :997 and `TestOlderBuildInPlaceRestore_SameDirDetected` (:1112) say otherwise.
    Every staged restore of an ABO archive in S2b–S7 is a foreign restore with nil coverage. When the hooks run
    (from S4 r2), every API key becomes HEAD's `inactive` and every session and one-time token is deleted
    (05-authn:1212-1216). The same happens at the roll-forward boot after an older build restored in place during
    a rollback.
  - 14:157-159 lists this as a residual "restore cost".
- **Evidence.**
  - At HEAD a restore keeps keys and sessions. This is a HEAD-parity change that users see: automation and scrape
    keys stop working after any restore.
  - Old row 20 carried its release note. A-2 and the integration pass deleted the row as "not a rollback cost"
    (14:388) but did not move the note anywhere. 09:156-158 now contradicts 05-backup.
- **Fix.**
  - Replace 09:156-158 with: "Restoring an ABO-format archive on S4 r2–S7, and the first S4 r2+ boot after an older
    build restored in place, disables every API key (HEAD's `inactive`; re-enable on the keys page) and signs
    everyone out; this is a restore cost (05-backup B7, K22), not a format change."
  - Add a release-note line to S4 r2 with that text, and cite it in the S4 table and 9.4.1's S4 r2 row.

### OPS9-6 — MAJOR — A-12 left done-checks with no environment: four need systemd running the committed unit, and one needs a network-reachable ABS client

- **Spec.**
  - **S0 done-check (09:959):** "a 12-minute fake migration under the unit is not killed".
  - **S1 (09:1001):** "under the committed unit (`Restart=on-failure`), staging a restore brings the service back".
  - **S2 (09:1018):** "`systemctl stop` during a running op … no SIGKILL in the journal".
  - **S2b (09:1031):** "under the committed unit, staging a reset brings the service back".
  - **Cell c (09:933) and P7 branch B (09:1271):** "live login and progress sync with a real ABS client, before and
    after rollback", gate S2, S4, S6 and S9.
  - **§9.3:855-861 (A-12):** staging is a `--network none` container. There is no ssh-reachable host, and "the
    reference production host is never the staging host". The fake-ssh harness returns canned output.
- **Evidence.**
  - None of these checks can run anywhere the spec defines. Docker does not run the committed systemd unit
    (`deploy/audiobook-organizer.service:81-112` @748fd787a).
  - A phone or desktop ABS client cannot reach a `--network none` container.
  - So S0, S1, S2, S2b and the four ABS-path stages cannot meet their written done-checks.
- **Fix.**
  - **09 §9.3 "Staging":**
    - Add a CI job, `unit-lifecycle`. The GitHub Ubuntu runner has systemd and passwordless `sudo`. The job
      installs the committed unit with a drop-in that overrides `ExecStart`/`DATABASE_PATH` only, and runs these
      four checks as `scripts/tests/test_unit_lifecycle.py`. Register it in `ci/gates.txt` and run it on flip PRs
      and nightly.
    - Or name a systemd-as-PID-1 container image, still `--network none`.
  - **P7 branch B:** replace the live client "before and after rollback" with a scripted ABS client inside the
    container, run through `docker exec`. It replays the login and progress-sync requests of the real client,
    recorded once by the owner. The real-client check becomes a post-deploy owner checkpoint on the reference
    host.

### OPS9-7 — MINOR — Early fix 5 locks out an install that has the Basic gate on and an empty saved password; the S0 note gives no recovery

- **Spec.**
  - 09:943 item (2): "Basic authentication with an empty saved password is refused".
  - 09:953: "No offline secret-recovery command is needed".
  - 05-authn §11.1 item 5: every non-exempt route answers 401.
- **Evidence.**
  - A UI save with no password writes an empty encrypted row when none exists
    (`internal/config/persistence.go:1996-2003` @748fd787a). Every decrypted secret row is applied, the empty one
    included (`:1193-1205`).
  - The blob beats `BASIC_AUTH_ENABLED` (09 §9.1:79). The stored row beats `BASIC_AUTH_PASSWORD`. Neither key is
    in `envLockedSettings` (`internal/config/config.go:2241-2253`).
  - The exempt set is health, assets and ABS. So after S0 no setting or variable reaches the Settings page, and
    the only way back is a rollback below S0.
- **Fix.**
  - Add a P-row before S0: from `GET /api/v1/config`, gate enabled with no password set.
  - Add this to the S0 release note: "If Basic authentication is on without a saved password, save one before
    upgrading. If you are locked out, set `BASIC_AUTH_PASSWORD` and restart." Then make the hotfix honour that
    variable when the stored password is empty, in memory only and logged. Alternatively, name the rollback as the
    recovery.
  - Correct 09:953.

### OPS9-8 — MAJOR — "The binary of the release that wrote the archive" cannot be identified, and nothing enforces it

- **Spec.** 09:886-888, 05-backup:619-620. Both subcommands "run with the binary of the release that wrote the
  archive", which "has no pending step on its own archive".
- **Evidence.**
  - Every archive before S8 is HEAD's `legacy-abo/0`, which records no writer (05-backup B7).
  - The reference host runs untagged `main` builds (P10), so no release artefact exists to download. A Docker
    build reports `commit ""` (09:954).
  - `provision.sh` therefore has no rule for picking the binary. A wrong pick is not always refused:
    - an **older** binary than the writer is admitted, because no level was raised and HEAD tolerates higher
      `db_version` (`internal/database/migrations.go:511-532`);
    - it then scrubs with its own, older knowledge of which families hold credentials;
    - so a credential family added later survives into the "inert" copy (R6-7).
- **Fix.**
  - `staging-restore` reads the restored store's served ledger `last.build`. That value has been written since S0
    (09:258, S0 "Data/keys") and equals `buildid.Binary`.
  - Both subcommands refuse unless the running binary's `buildid.Binary` equals it. `--build-from-commit` prints
    the commit to build.
  - `provision.sh` builds that commit, or copies the binary from the reference host read-only. Say which.
  - Add the mismatch case to `test_staging_scrub.py`'s S0 cases.

### OPS9-9 — MINOR — 09 says a pre-S0 archive is staged with `cur`; 05-storage and 05-backup say `staging-restore` refuses it

- **Spec.** The three texts disagree:
  - 09:888: "for a pre-S0 archive they run with `cur`, whose open is then a plain open".
  - 05-storage:1026 (also `TestStagingRestoreWritesMarker`, :2486): `staging-restore` "refuses an archive whose
    store has no `c/storage/install`".
  - 05-backup:614-615 agrees with 05-storage.
- **Evidence.**
  - At S0 every production archive is pre-S0. So S0 has no staging copy.
  - §9.3:828 nonetheless puts the boot budget and the real-data R-test into "every stage's Done when".
- **Fix.**
  - Delete the 09:888 clause.
  - State in §9.3 that staging needs an archive made after the reference host first ran S0, so S0's done-check has
    no staging data point. §9.3:850 already makes S1's measurement the first.

### OPS9-10 — MINOR — `compat-rehearsal` never runs on S2b r2, S5 r2–r7 or the floor release

- **Spec.**
  - 09:221: the trigger is "a PR whose diff changes `shipped_stage:`".
  - 9.4.1: T7 is "yes (schedule keys, aliases)", and T8 is the floor (deletes code, declares `UpgradeFloor`).
  - S2b r2 makes strict mode the default.
- **Evidence.**
  - None of these changes `shipped_stage:`, so their rollbacks are never rehearsed by a required check. Only the
    nightly run, which gates nothing, covers them.
  - These are the releases whose rollback 05-tasks treats as most delicate: clocks, and the binary swap to a
    release at or above the stepping stone.
- **Fix.** Trigger `compat-rehearsal` on a change of `release:` as well. Require that the T7 schedule-conversion PR
  and the T8 PR are the PRs that change `release:` for their releases (S5's table names them). This keeps A-13's
  one-PR-per-release cost.

### OPS9-11 — MINOR — S2's `TimeoutStopSec=30` applies to older builds after a rollback; HEAD's 30 s drain plus store close can exceed it

- **Spec.** 09:1012. `TimeoutStopSec=30` and `KillMode=mixed` in the committed unit; `stop_grace_period: 30s` in
  compose.
- **Evidence.**
  - HEAD's stop sleeps 0.5 s, then drains HTTP for up to 30 s, then closes the stores
    (`internal/server/server_lifecycle.go:572,582` @748fd787a).
  - A long download or stream keeps the drain at its cap. So a HEAD, S0 or S1 binary rolled back onto the S2 unit
    is sent SIGKILL before it closes the stores and stamps its operations.
  - HEAD's unit sets no stop timeout, so systemd's default of 90 s applies. No §9.2.1 row says this.
- **Fix.** Set `TimeoutStopSec=60` and `stop_grace_period: 60s`: at least the slowest build that may run under the
  unit, while core's own budget stays 25 s. Otherwise add a §9.2.1 row, From S2.

### OPS9-12 — MINOR — Ops records go into the embedded compat ledger, so every one of them becomes a code change

- **Spec.** Several records are written to "the stage's ledger section":
  - the boot-budget results (09:845);
  - the move-window announcements (09:783);
  - the CI p50 (09:799);
  - both values of the generated counts (09:834);
  - the S0 Dockerfile facts.

  That section lives in `docs/compat-surfaces.md`. `internal/books` embeds a byte-equal copy of it
  (`TestCompatLedgerCopyFresh`, 09:727-731), and `compat.Ledger` parses that copy at boot.
- **Evidence.**
  - Every boot measurement or window announcement becomes a non-doc PR. It runs full CI and `docker-image`, mints
    an RC and counts toward auto-promotion.
  - A malformed note can break the boot-time parse of an artefact that ships to users.
- **Fix.** Keep `docs/compat-surfaces.md` for surface rows only. Move the per-stage ops records to a non-embedded
  `docs/plans/platform-core/stage-ledger/<stage>.md`, which is doc-only. Add a ledger-parser test that rejects
  non-row content.

### OPS9-13 — MINOR — The 35-minute CI budget has no slack, its remedy conflicts with required checks, and its p50 is measured only once

- **Spec.** 09:795-800. The budget is "the longest job timeout `ci.yml` sets today". A PR over the budget "moves
  work to the nightly run or adds caching". S0 records the "p50 of the first 20 non-doc PRs".
- **Evidence.**
  - Both 35-minute caps already hold a 25-minute `go test -timeout` plus setup (`ci.yml:853-859` @748fd787a).
  - The new required work cannot move to nightly without breaking a binding resolution: `TestBootCrashMatrix`
    (A-8), `TestLegacyEnvDecodeMatchesHEAD` (A-7), the prev-build tests and `check_soak.py` (A-9).
  - `prev` is rebuilt cold whenever `PREV_REF` or `ci/prev_ref.txt` changes.
  - No later p50 is defined, and nobody owns measuring it.
- **Fix.**
  - Split `extraction-gates` into parallel jobs, each with `timeout-minutes` ≤ 30.
  - List the items that may move to nightly; exclude the A-7, A-8 and A-9 items and the prev-build tests.
  - Make the remedy sharding or caching.
  - Define a weekly p50 computed from the workflow run history by a committed script.

### OPS9-14 — MINOR — The release-ID grammar is not closed (spelling, "…", bootstrap case)

- **Spec.** 09:191-193 lists `S2r1 … S2b-r1 … S4r1 … S5-r1 < S5-r2 < … < floor`, mixing the spellings with and
  without a hyphen.
- **Evidence.**
  - "Refuses an unknown or backwards value" needs a closed grammar, which the list does not give.
  - The PR that first commits `ci/stage.txt` (absent → `S0`) is not among `test_check_soak.py`'s cases
    (09:1354).
- **Fix.**
  - Give the regex, e.g. `S0|S1|S2r[1-3]|S2br[12]|S3|S4r[12]|S5r[1-9]|floor|S6|…|S10`, and the order rule:
    `S5r<n>` increases by one, `floor` follows any `S5r<n>`.
  - Use that spelling in 09, 05-storage, 14 and `soak_record.py`.
  - Add the creation case to the tests.

### OPS9-15 — MINOR — Stale cross-references: the S4 overview omits row 3, and the owner-checkpoint list names S8 instead of S1

- **Evidence.**
  - Row 3's "From" includes S4, and the rule at 09:141-142 says the overview lists it. The §9.3 S4 row (09:812)
    lists only row 4; the S4 Rollback row cites row 3.
  - With A-13's deletion of `check_rollback_rows.py`, nothing catches this kind of drift.
  - 09:208-210 names "the owner actions of S0, S2 and S8". S8's unit migration is deferred (A-6), so S8 has none.
    S1 has one: copy the recipe again (S1 "Deploy").
- **Fix.**
  - The S4 overview lists "rows 3 (auth mode), 4".
  - The owner-checkpoint list reads "S0, S1 and S2".
  - Optionally, restore a three-line test that compares each row's "From" with the §9.3 cells.

### OPS9-16 — MINOR — ADV-11 residue: `TestUpgradeFloor_RefusedBelowBooks66` and P13 "books ≥ 66"

- **Spec.**
  - 09:290-295: `LegacyCap` is the highest HEAD-style step at the S2b flip (66 at the evidence commit). Steps 67 and
    up are allowed until S2b.
  - The literal 66 still appears at 09:1191, 09:1355, 09:1277 (P13) and 05-tasks:1889,1958.
- **Fix.**
  - Rename the test `TestUpgradeFloor_RefusedBelowLegacyCap`.
  - P13 becomes "the store meets the floor `--print-upgrade-floor` prints".

### OPS9-17 — MINOR — S8 updater move: ABO's source wiring is untested, row 2 mislabels `core.updater.*`, and S8's new state is unlisted

- **Evidence.**
  - **Wiring is untested.**
    - The S8 done-check re-points S0's tests at `core/updater` with a test source (09:1139; 05-updater:522-525).
    - ABO's `main` passes "its slug, binary name and asset namer" (05-updater:513), and open item 3 says the namer
      is unverified.
    - Nothing compares that wiring with HEAD's (`internal/updater/updater.go:36,71,115,318-326` @748fd787a).
    - A wrong namer makes every check report nothing and every manual apply fail.
  - **Row 2 contradicts 05-config and 05-updater.** Row 2 (09:147) lists `core.updater.*` as keys "with no legacy
    name" from S3, while 05-config:1683 and 05-updater:451-453,519 alias `auto_update.*` to `core.updater.*` at S8.
  - **S8's new state is unlisted.** S8 writes `c/updater/seen` (05-updater:287) and the durable `core.updater.check`
    clock. Neither is in the §9.3 S8 "native truth" cell or §9.2.3's core-rows line.
- **Fix.**
  - Add `TestABOUpdaterSourceMatchesHEAD` to the S8 done-check: the options ABO's `main` builds produce HEAD's API
    path, asset name per GOOS/GOARCH and user agent, and S0's fake-server test runs through them with only the base
    URL injected.
  - Replace row 2's example with a key that has no alias (`core.updater.mode`).
  - List `c/updater/` in both places.

### OPS9-18 — MINOR — `prerelease.yml`: bookkeeping-only PRs mint RCs, and S0's added Docker build is redundant

- **Evidence.**
  - `paths-ignore` covers only `**.md` and `docs/**` (`.github/workflows/prerelease.yml:9-12` @748fd787a). So a
    PR that changes only `ci/soak.txt` or `ci/prev_ref.txt` (the owner's bookkeeping) mints an RC and counts
    toward the 10-RC promotion.
  - Bookkeeping alone can therefore cut a production minor. It also runs full CI and `docker-image`, because the
    classifier sets `go=true` (`ci.yml:92`).
  - S0's "`prerelease.yml` gains a Docker build without push" (09:947) duplicates the required `docker-image` PR
    job. It re-adds the Docker path the workflow deliberately disabled (`prerelease.yml:41-49`), and 09 does not
    say whether its failure blocks the RC or `check-rc-ordinal`.
- **Fix.**
  - Add `ci/soak.txt` and `ci/prev_ref.txt` to `paths-ignore`.
  - Classify both as needing only `extraction-gates`, so `check_soak.py` still enforces "alone".
  - Drop the prerelease Docker build, or specify it as a separate, non-blocking job that `check-rc-ordinal` does not
    `need`.
