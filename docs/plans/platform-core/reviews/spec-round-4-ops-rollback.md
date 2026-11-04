<!-- file: docs/plans/platform-core/reviews/spec-round-4-ops-rollback.md -->
<!-- version: 1.0.0 -->
<!-- guid: c69dde7e-138b-439f-8e1e-17edb9cc940a -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 4: ops, rollback and incremental delivery (lens OPS4)

Lens: walk S0–S10 of [09](../../../specs/platform-core/09-abo-on-core.md) v1.3.1 (S2b and UI-0 included),
checking for each stage that it can ship, that its done-check is measurable, and that a binary-swap
rollback works with exactly the three costs §9.2.1 states. Also covered: release gating as one `make ci`
requirement (R3-2), the state-directory fallback on Docker and launchd (R3-4), the updater's P−1 `Boot`
and `binary_not_writable`, min-upgrade-from through the store sidecar, the boot-time budget, and the
deploy files. Code anchors are `path:line @748fd787a`. Spec anchors are `file §section (line)` against
the current checkout (09 v1.3.1, 05-config, 05-updater v1.4.1, 05-storage, 05-tasks, 06, 14).

R3-1 (one stored format, binary-swap rollback) and R3-2 (one gating requirement) are taken as binding.
The findings below attack only places where the revised text does not work as written.

Counts: **1 BLOCKER, 6 MAJOR, 10 MINOR** (17 findings).

## A. Regression check (round-3 OPS BLOCKER and MAJOR findings)

| ID | Status in round 4 | Open finding |
|---|---|---|
| OPS3-1 state-dir fallback (BLOCKER) | Fixed in 05-config C15/§1.7, 06 §6.4.5, 09 S2 and `TestStateDirFallbackMatchesHEAD`. The added "set the canonical variable in the Dockerfile" step introduces a new defect | OPS4-2 |
| OPS3-2 S0 blanks secrets | Fixed (09 S0 "Data/keys": nothing is moved or blanked before S3) | — |
| OPS3-3 gate path list / admin bypass | Resolved by R3-2. The single requirement cannot be checked as written | OPS4-7 |
| OPS3-4 RC promotion gate | Resolved by R3-2 (promotion mechanics removed). One promotion-time sentence remains and has no mechanism | OPS4-6 |
| OPS3-5 Teach build in one production release | Resolved by R3-1 (no Teach builds, no floors) | — |
| OPS3-6 `PREV_REF` below the floor | Resolved by R3-1 (no level is raised, so `prev` always opens the store) | — |
| OPS3-7 store-based `min_upgrade_from` | Mechanism fixed (`StoreState` over `<db>.storage-applied`, `steps` in the requirement, P13). Its central term now means two different releases | OPS4-5 |
| OPS3-8 C7 mirror stop | Resolved by R3-1 (no mirror) | — |
| OPS3-9 `deploy-wait-ready.sh` cannot see `systemctl` | Script fixed. The recipes are never told to pass the new arguments | OPS4-10 |
| OPS3-10 boot budget interval | Fixed (A/B per stage, restart → ready). Execution details remain | OPS4-12 |
| OPS3-11 S9 reload check | Fixed (07 §7.5.2 case 2; 09 S9 rollback row) | — |
| OPS3-12 S0 updater hotfix | Fixed for both apply paths and the reader check. The semver comparison does not order `git describe` builds | OPS4-17 |
| OPS3-13 restore probe over ssh | Half-fixed. The probe cannot tell "absent" from "cannot run", 09 and 05-storage specify different probes, and S0 runs it before its sudoers lines exist | OPS4-3 |
| OPS3-14 deferred-contract checkpoint | Fixed (§9.2.2: a checkpoint only when a step will actually run; reuse limited to a crash loop of the same attempt) | — |

## B. Findings

### OPS4-1 — BLOCKER — After "move on first write", rolling back leaves an enabled Basic gate with an empty password, and the older build accepts `<username>:` (fail-open)

- **Spec:** 09 §9.2.1 "Rollback = binary swap", second bullet (lines 137-139: "its legacy copy is blanked
  in the same batch, so the older build sees it unset; re-enter it"); 09 S3 "Rollback" (line 659);
  05-config §1.12.2 "Secrets: moved on first write" (lines 1031-1037: "the blob field is written empty and
  the legacy row deleted"); 05-http-api S3 row (line 955: "a Basic password first written on S3+ is
  re-entered on the older build").
- **Evidence:**
  - `basic_auth_password` is one of the four `.encryption_key` rows (`internal/config/persistence.go:1989-1994`).
    Its switch `basic_auth_enabled` is a plain blob field, which S3 keeps writing in place.
  - The rollback target of S3 is S2, whose Basic gate is still gin's (09 S2 "HTTP": "The Basic gate stays in
    gin until S3"). That gate checks only `BasicAuthEnabled`, then compares the presented credentials with
    `config.AppConfig.BasicAuthPassword` using `subtle.ConstantTimeCompare`
    (`internal/server/middleware/basicauth.go:26-29,80-90`). With an empty expected password, a request with
    `Authorization: Basic <base64 "admin:">` matches.
  - So an operator who sets or changes the Basic password in the UI on S3 or later, then rolls back, exposes
    every non-exempt route to anyone who knows the username, which defaults to `admin` (compose
    `BASIC_AUTH_USERNAME:-admin`, `docker-compose.yml:21`). On installs where Basic is the only gate
    (`enable_auth` off), that is the whole app.
  - The release note calls this "re-enter the secret". It is a security hole that lasts until someone
    notices.
  - The same rule weakens another legacy check without failing: a webhook HMAC secret that has been moved
    makes the older build send deliveries with no `X-Audiobook-Signature-256` header
    (`internal/plugins/webhook/plugin.go:108-127`).
- **Fix:**
  1. Add a fail-closed Basic gate to the S0 hotfix set (09 S0 "Hotfixes", 05-authn §11.1): when
     `basic_auth_enabled` is true and the effective password is empty, every non-exempt route answers 401,
     health reports Degraded `basic_auth_no_password`, and the startup log names the setting. Add the test
     `TestBasicGate_EmptyPasswordRefuses`.
  2. Preferred: exempt `basic_auth_password` from the move for the extraction. The core build writes it in
     place into its existing encrypted legacy row with the `.encryption_key` material. That row is already
     encrypted at rest, so N10/G12 hold, and `k0-legacy` stays read-write for exactly the four legacy rows
     until the config cutover. Then no rollback target from HEAD on loses it.
  3. Alternatively, keep the move, and require (1) to ship in a release that is older than S3 by at least one
     production minor. K21 then names that release as the oldest safe rollback target for Basic-gated
     installs.
  4. In 05-config §1.12.2, list the secrets whose empty value weakens a legacy check (the Basic password and
     the webhook secret) and give each the treatment chosen in (2) or (3).
  5. Add the R-test row "Basic password saved on S3, roll back to S2: a request with an empty password gets
     401, not 200".

### OPS4-2 — MAJOR — Setting the state-directory variable in the Dockerfile overrides HEAD's fallback; a container whose `--db` is not under the data volume loses its keyring on recreate and then refuses to boot

- **Spec:** 09 S2 "Deploy" (line 622: "The shipped Dockerfile (`ENV`), compose file and plist also set the
  canonical state-directory variable to the database's directory"); 05-config §1.7 (lines 770-783); 05-config
  C15 ("An explicit value never falls back"); 05-config §2.4 keyring-open rules (lines 1433-1437:
  "keyring absent, stored records exist → `ErrKeyMissing`").
- **Evidence:**
  - The Dockerfile hard-codes `--db` only in `CMD`, and declares the data path as a `VOLUME`
    (`Dockerfile:92-96,104`). A user who runs `serve --db <other volume>/x.pebble` (an overridden `CMD` or a
    compose `command:`) gets, at HEAD, the key next to that DB through the fallback
    (`internal/config/state_dir.go:143-153`).
  - With `ENV <canonical>=/data`, the state directory is explicit and never falls back. The keyring,
    `config.boot.json` and the restore journal go to `/data`, which for that user is an **anonymous** volume.
  - `k0-legacy` is still found through the legacy directory (05-config `Dirs`, line 1316), so S3 boots and
    moves secrets into `c/secrets/`. After `docker run` of a new container, or `compose down && up`, the
    anonymous volume is new. The keyring is absent and records exist, so boot fails with `ErrKeyMissing` and
    crash-loops under `restart: unless-stopped`, and every secret moved so far is unrecoverable unless it was
    exported.
  - S2–S3 also run two resolvers. ABO's `serve` still calls HEAD's `EnsureSecureStateDir`
    (`cmd/root.go:336`), which reads only `ABK_STATE_DIR`, never the canonical name. With the `ENV` set, the
    kernel's directory (`/data`) and ABO's directory (the DB's directory) diverge whenever the DB is not in
    `/data`.
  - The fallback already yields `/data` for the default invocation, so the `ENV` adds nothing there. The
    plist case duplicates `DATABASE_PATH`'s directory in a second literal, which is two sources for one fact
    (COR2-8's own principle).
  - The image job's new assertion (09 S0, line 558, "keyring is on the data volume") passes trivially and
    misses this case.
- **Fix:**
  - Drop the `ENV` from the Dockerfile and the variable from the plist, and rely on the fallback.
    `TestStateDirFallbackMatchesHEAD` already pins it.
  - If an explicit value is wanted for discoverability, set it only in `docker-compose.yml`, next to the named
    volume and a `command:` that names the same directory.
  - Extend the S0 image job with a second run: `--db /config/x.pebble` on a named `/config` volume. Seed,
    boot S3, recreate the container, and assert that the keyring is under `/config` and that secrets
    decrypt.
  - Correct 05-config §1.7's last sentence and 09 §9.2.5 line 352 ("the canonical state-directory variable
    that S2 adds to the shipped files").

### OPS4-3 — MAJOR — OPS3-13 half-fixed: the restore probe refuses every rollback on stores that never staged a restore, and refuses at S0 before its sudoers lines exist

- **Spec:** 09 §9.2.2 "Rollback tooling" (lines 246-255: "a literal sudoers line per file, `/usr/bin/test -e
  <state_dir>/restore/pending.json` and the same for an in-flight restore journal … owner actions: … in S1, the
  restore-probe lines"); 05-storage §5S.3.7 (lines 972-979: `test -e …pending.json`;
  `find <state_dir>/restore -mindepth 2 -maxdepth 2 -name inflight.json -print -quit`; "a probe that cannot
  run … is `cannot_determine`, which refuses"); 09 S0 "Done when" (line 569: the script "covers the compat and
  restore-probe cases"; "`make rollback` on staging S0 → HEAD … passes the guard").
- **Evidence:**
  - **Missing directory.** `<state_dir>/restore/` is created only when a restore is staged (05-backup §5B.3.4
    step 8). On every other store, `find <state_dir>/restore …` exits 1 with "No such file or directory".
    That is the shape of "cannot run", so the guard refuses every rollback unless the operator sets
    `ROLLBACK_IGNORE_RESTORE=1`.
  - **Ambiguous exit code.** `sudo -n /usr/bin/test -e P` exits 1 both when P is absent and when sudo has no
    rule ("a password is required"). The spec does not say how the script separates the two.
  - **Two probes.** 09 asks for a literal `test -e` line for the in-flight journal, which is impossible: its
    path contains the restore ID (`restore/<id>/inflight.json`). 05-storage uses `find`.
  - **Order of stages.** The S0 guard ships the probe, but the sudoers lines are an S1 owner action, so S0's
    own done-check ("S0 → HEAD passes the guard") fails by default.
  - The existing recipe runs every probe as the ssh user with `sudo -n` (`Makefile:899-913`;
    `docs/system/deploy-and-gpu-ops.md:195-218`).
- **Fix:**
  - Use one probe that exits 0 whenever the state directory is readable:
    `sudo -n /usr/bin/find <state_dir> -maxdepth 3 ( -path <state_dir>/restore/pending.json -o -path '<state_dir>/restore/*/inflight.json' ) -print -quit`.
    Non-empty stdout means pending or in flight. A non-zero exit, or stderr starting with `sudo:`, means
    `cannot_determine`.
  - Make this one literal sudoers line an **S0** owner action, next to the `.storage-levels` and
    `.storage-applied` lines.
  - Replace 09 §9.2.2's `test -e` text with a reference to 05-storage.
  - Add cases to `scripts/test_storage_format_guard.py`: "no `restore/` directory → pass", "no sudoers line →
    refuse naming it", "inflight under any ID → refuse".

### OPS4-4 — MAJOR — The S8 service-owned binary directory `<state-dir>/bin` breaks `make rollback`: its probes run unprivileged and cannot enter the 0700 state directory

- **Spec:** 05-updater §5U.5 "Supported install layouts" (lines 421-433: layout (a) "e.g.
  `<state-dir>/bin/<binary>` … (and by `make deploy`/`make rollback`, which then need no root for the copy)");
  09 S8 "Preconditions" (line 726: owner checklist, "S8 commits the unit with that layout as default").
- **Evidence:**
  - The state directory is created 0700 and holds credentials (`internal/config/state_dir.go:112-116,135`).
    On the committed unit it is also `HOME`, `WorkingDirectory` and the DB's parent
    (`deploy/audiobook-organizer.service:95-98`).
  - `make rollback` runs `test -f $(DEPLOY_BIN).prev`, `$(DEPLOY_BIN).prev --print-storage-format` and
    `$(DEPLOY_BIN) --print-storage-format` as the ssh deploy user **without sudo** (`Makefile:899-902`).
    `deploy-match-unit-type.sh` runs `.prev --print-capabilities` the same way.
  - The new guard adds `--print-storage-levels` and `--print-upgrade-floor` probes (05-storage §5S.3.7).
  - With `DEPLOY_BIN` inside a 0700 directory owned by the service user, each probe fails. The recipe
    reports "no .prev found" and every rollback from S8 on refuses.
  - "Need no root for the copy" is false: the recipes run as the deploy user, not the service user.
- **Fix:**
  - Layout (a) is a dedicated directory outside the state directory, owned by the service user, mode 0755,
    binaries 0755 (for example a service-owned `bin` under the install prefix).
  - The recipes keep unprivileged probes and copy with literal sudoers lines (`cp` as the service user or as
    root).
  - The S8 checklist gains the plist's `ProgramArguments` path (on macOS the shipped
    `/usr/local/bin/audiobook-organizer` may be root-owned; `deploy/com.audiobook-organizer.plist:30-33`).
  - The checklist's verification is "a dry-run `make rollback` **run as the deploy user** prints the `.prev`
    levels".
  - Delete the "no root for the copy" clause from 05-updater.

### OPS4-5 — MAJOR — "Minimum-upgrade-from release" names two different releases across 09, 05-tasks and 05-storage; read one way, the stepping stone is a release that itself refuses the store

- **Spec:** 09 §9.2.2 (line 220: legacy-step code "is **kept until the minimum-upgrade-from release**;
  05-tasks T8 may delete it only from that release on"; lines 223-226: the bound "naming the last production
  release before it, which still carries legacy steps 59–66"); 09 S5 "Moves and batches" (line 685: "T8 ships
  from the minimum-upgrade-from release on"); 09 §9.4.1 T8 row (line 785: T8 deletes "the legacy-step store
  methods"); 05-tasks §5T.6 (lines 1186-1189: the release that deletes them "**is** the declared
  minimum-upgrade-from release"); 05-tasks §5T.8 T8 row (line 1255: "**Not** deleted by T8: … the store methods
  books legacy steps 62/65/66 call; they stay until the declared minimum-upgrade-from release"); 05-storage
  §5S.3.5 (line 665: `InstallFirst string // the release to install first (09's minimum-upgrade-from)`);
  04 line 701.
- **Evidence:**
  - The manifest field and `InstallFirst` use the term for the release to install **first**, which carries
    the steps.
  - 05-tasks line 1188 and 09 line 220 use it for the release that **deletes** them.
  - 05-tasks contradicts itself on whether T8 deletes the methods at all (line 1188 vs line 1255).
  - An implementer who follows "T8 ships from the minimum-upgrade-from release on", with that release = the
    `InstallFirst` release, puts the deletion into the stepping stone. A pre-66 store told to "install X
    first" is then refused by X with `ErrUpgradeFloor` naming X.
  - "The last production release before it" also cannot be known when the T8 PR is written, because
    production minors are auto-cut (§9.1).
- **Fix:**
  - Use two terms everywhere:
    - **stepping-stone release** (`InstallFirst`, the manifest's `release`): carries books steps 1..66 and
      the store methods they call, and contains no T8 deletion;
    - **floor release**: the first release with the deletion and books' `UpgradeFloor{Version: 66}`.
  - Rewrite 09 line 220 ("kept in every release before the floor release"), 09 line 685, 05-tasks lines
    1186-1189 and 1255, 05-storage line 665, 02 N14 and 04 line 701 accordingly.
  - Set `InstallFirst` to the newest production tag that exists when the T8 PR merges.
  - Add `TestStepSteppingStoneValid` in root CI: the named tag exists, predates the T8 commit, and its binary
    registers books 66 and prints an empty `--print-upgrade-floor`.

### OPS4-6 — MAJOR — The rollback rehearsal gates nothing: it is outside `make ci`, and "a red run blocks that stage's next release" has no mechanism

- **Spec:** 09 §9.0 (lines 24-25: "the CI-checkable proof of G5 is the rollback rehearsal
  `make compat-rehearsal`"); §9.2.1 (lines 175-176: it "runs nightly and in every stage's done-check; a red run
  blocks that stage's next release"); S0 job table row `compat-rehearsal` (line 595); 05-authn §10
  `TestOneFormat_LegacyRowsReadByPrevBuild` and 05-storage `TestOneFormat_PrevBuildReadsGranteeWrites`
  (where they run is not stated).
- **Evidence:**
  - R3-2 makes `make ci` the only gate and removes promotion-time mechanics.
  - Releases are still cut automatically: an RC on every push, and a production minor dispatched every 10
    RCs (`.github/workflows/prerelease.yml:59-131`; 09 §9.1 last row).
  - Nothing reads the nightly result, so "blocks the next release" cannot happen. A PR that breaks
    "the previous release reads every row" (the property R3-1 rests on) merges green, reaches RC and
    `releases/latest`, and is found by the nightly run afterwards.
  - The prev-binary one-format tests need a `prev` build, and no file says whether `make ci` builds one.
- **Fix:**
  - Put the one-format tests (05-authn, 05-storage, 05-config §1.12.2 rows, 05-tasks §5T.9) into the
    `make ci` requirement, with `prev` built from `PREV_REF` and cached by tag, so every Go PR runs them.
  - Replace "blocks that stage's next release" with a merge-time rule that fits R3-2: `extraction-gates` fails
    a stage's flip PR while the latest nightly `compat-rehearsal` on `main` is red.
  - A red nightly run opens or updates one issue.

### OPS4-7 — MAJOR — "Every PR passes root `make ci`" cannot be enforced as specified: `make ci` is a local target, the Docker job is listed as part of it but is a separate path-filtered job, and rebase merges test a different tree

- **Spec:** 09 §9.2.1 (lines 167-176: `make ci` "includes … `check_module_skew.py` and the Docker image build");
  09 S0 "Gates" and "Docker" rows (lines 557-558: the image build is "a PR CI job [that] runs on every PR that
  touches a non-doc file"); the S0 `make ci` table (lines 571-596, no Docker target); 14 Q-I (line 80: option
  (a) required check with no bypass; option (b) "a red commit reaches RC installs before the backstop reverts
  it").
- **Evidence:**
  - `make ci` is a Makefile target (`Makefile:785`). CI on PRs runs `ci.yml` jobs (`.github/workflows/ci.yml:24-29`).
    A ruleset requires check **names**, not a make target.
  - Putting a full image build, run and seeded-volume boot into the local target would make every worktree's
    local gate need Docker. `/parallel-sweep` runs `make ci` in each worktree
    (`.claude/skills/parallel-sweep-impl/references/coordinator-prompt.md:111`).
  - A required check whose workflow is skipped by a workflow-level `paths:` filter never reports, so doc-only
    PRs stay "Expected — waiting" forever.
  - The repo merges by rebase only (CLAUDE.md Quick Fix step 5). Without "require branches to be up to date"
    or a merge queue, the commit on `main` is not the tree that was tested, so "a release then only contains
    commits that passed" does not follow.
  - Under Q-I (b), 14 itself says the claim is false.
- **Fix:**
  - Define the requirement in 09 §9.2.1 and 14 Q-I as a named list of required checks: the `ci.yml` jobs that
    mirror `make ci`, `extraction-gates`, and `docker-image`.
  - Path-skipped jobs use a job-level `if:` on the `changes` classifier, so a skip reports success.
  - Require strict up-to-date branches or a merge queue.
  - `make ci` stays the local mirror, without Docker.
  - 09 states that under Q-I (b) releases can contain unchecked commits.

### OPS4-8 — MINOR — State-directory resolution: P−1 is said to take its DB input from a marker inside the directory it is resolving, and S2–S3 run two resolvers that can disagree

- **Spec:** 05-config §1.3.3 `ResolveStateDir` comment (lines 288-293: "P−1 passes the db_path recorded in its
  marker") and §1.7 step 3 (line 777); 06 §6.4.5 `StateDir` (lines 747-748) and §6.5.1 P−1 row (line 1193);
  05-updater `Boot` comment (lines 248-252).
- **Evidence:**
  - The marker is `<state_dir>/updates/pending.json`. The state directory is resolved once, before P−1
    (05-config line 641), so the clause is circular and contradicts "whose result P−1, P0a, P0 and every
    consumer share".
  - Separately, from S2 to S3 ABO's `serve` still resolves through HEAD's `EnsureSecureStateDir`
    (`cmd/root.go:336`). That resolver uses `AppConfig.DatabasePath` (YAML included) and `ABK_STATE_DIR`.
    The kernel uses flags and env only.
  - An S1-staged restore intent written by ABO into one directory can be looked for by S2b's P0a in another.
- **Fix:**
  - Delete the marker clause from all four places. The marker's `db_path` is only `ReaderCheck`'s DB path.
  - From S2, ABO's legacy code takes the kernel-resolved directory (an `app` accessor passed into `serve`)
    instead of calling `EnsureSecureStateDir`.
  - Add a test: YAML-only DB path plus an uncreatable default gives one directory for the boot record, the
    restore intent and `.encryption_key`, or the boot error of 05-config step 2.

### OPS4-9 — MINOR — Updater `Boot` trusts a stale marker: nothing checks that the running binary is the marker's `to_build`

- **Spec:** 05-updater §5U.4 "Confirm / roll back" (it increments `boots` "first, so every boot that ends
  before confirmation counts as a failed boot"); 06 §6.5.1 P−1.
- **Evidence:**
  - `make rollback` or `make deploy` within the 2-minute confirmation window, or a rollback to a pre-S8 build
    (which never deletes the marker), leaves `updates/pending.json` in place.
  - The next S8+ boot of a **different** binary counts its starts against the old update. After
    `max_boot_attempts` restarts it "rolls back" by moving the running binary to `.failed` and recording the
    wrong version in `history.jsonl`, which the scheduled-apply skip then reads.
- **Fix:**
  - `Boot` compares the marker's `to_build` with its own build ID. On a mismatch it appends the marker to
    `history.jsonl` as `superseded` and counts nothing.
  - Add `TestBoot_IgnoresMarkerOfAnotherBuild`.

### OPS4-10 — MINOR — The S0 and S1 recipe changes never reach the operator's recipes

- **Spec:** 09 S0 "Deploy" (line 567: new `deploy-wait-ready.sh BASE_URL TIMEOUT [SSH_HOST UNIT]`); §9.3 (lines
  538-540: `DEPLOY_READY_TIMEOUT` in `rollback` and `deploy`); S1 "Deploy" (line 605: the `deploy` recipe in
  `Makefile.local.example` refuses while a restore is staged); §9.0 (line 23: the `deploy` target "is not
  committed").
- **Evidence:**
  - `make rollback` calls `deploy-wait-ready.sh $(DEPLOY_URL) 300` (`Makefile:916`). Without `SSH_HOST UNIT`
    the script falls back to URL-only polling, so the OPS3-9 fix is dead unless the recipe passes
    `$(DEPLOY_HOST) audiobook-organizer.service`.
  - The real `deploy` recipe is the operator's uncommitted `Makefile.local`. Changing the template does not
    reach it, so the restore refusal (OPS2-26) and the long-start handling do not apply to real deploys.
- **Fix:**
  - S0 states the `rollback` recipe's new call with the host and unit.
  - Add owner actions: S0 "copy the updated `deploy` recipe from `Makefile.local.example` into
    `Makefile.local` (timeout variable, ssh arguments)" and S1 "the same for the restore probe".
  - `deploy-preflight.sh` warns when `Makefile.local`'s `deploy` lacks `DEPLOY_STATE_DIR`.

### OPS4-11 — MINOR — The "stated costs" of a rollback are not one list: 01 names two, and several observable consequences are listed nowhere, although the R-test asserts "exactly those of §9.2.1"

- **Spec:** 01 goal 3 (line 50: "a rollback's only stated costs are one re-login and re-entering secrets");
  09 §9.2.1 (lines 132-149: three costs plus two consequences); 09 §9.10 R-test ("the stated costs are exactly
  those of §9.2.1"); 14 K13.
- **Evidence:** consequences that are not in §9.2.1:
  - 05-tasks T2 row: "an `opstate:` → checkpoint move resumes from zero once after a rollback";
  - S6/S7 outbox webhooks are delayed or dead-lettered after 24 h (09 S7 "Rollback");
  - an integration URL edited on the older build resets `Local` on roll-forward (S7);
  - `/books/...` bookmarks 404 (S9);
  - unsigned webhook deliveries (OPS4-1);
  - a secret cleared on core can come back on the older build from the gap-fill file `<db dir>/config.yaml`,
    which older builds read when the blob field is empty (05-config lines 986, 1008, 1034);
  - an older build's `system/reset` or factory reset leaves `c/secrets/` and `c/config/`, and on roll-forward
    those values win again (05-config line 1035: a core record is used when the legacy copy is empty).
- **Fix:**
  - Make §9.2.1 one complete table (cost or consequence, stage, release-note text), and have the R-test assert
    that table.
  - 01 and 14 K13 refer to it instead of restating it.
  - For the reset case, `auth`/`config` P4d drop core-only rows whose legacy family was emptied by a foreign
    open (foreign-open record present, legacy family empty). Otherwise state it as a cost.

### OPS4-12 — MINOR — Boot-time budget: no runnable procedure, an ordering bias, and a noise level larger than the threshold

- **Spec:** 09 §9.3 "Boot-time budget" (lines 531-540).
- **Evidence:**
  - "Boot `prev` and `cur` three times each" on one restored copy names no script and no result location
    beyond "the ledger".
  - Running all `prev` boots first gives `cur` a warm page cache.
  - With 130–200 s warm-ups (§9.1), the median of three samples cannot resolve a 10 % threshold reliably.
  - HEAD's `/health` already reports `warmup_ms` (`internal/server/handlers/system/handler.go:268-272`), which
    the spec does not use.
- **Fix:**
  - Add `scripts/boot_budget.py --prev <bin> --cur <bin> --db-copy <dir> --runs 5`. It interleaves
    A/B/A/B, discards the first boot of each binary, records restart→ready and `warmup_ms`, and appends the
    result to the stage's ledger section.
  - The threshold is p50(cur) ≤ max(1.10 × p50(prev), p50(prev) + 15 s).

### OPS4-13 — MINOR — Done-checks name a "staging" host that no document defines

- **Spec:** 09 S0 "Done when" (line 569: "`make rollback` on staging S0 → HEAD and HEAD → S0"); S8 "Done when"
  (line 735: "a staging install with `auto_update.enabled=true` … receives the next signed release"); §9.11 S8
  row.
- **Evidence:** The repo documents one reference production host that deploys `main` (09 §9.0;
  `Makefile.local.example:88-110`). It describes no staging host, so the two checks cannot be run as written.
- **Fix:** Define "staging" once in 09 §9.3: a disposable systemd host or container provisioned by a
  committed script with the committed unit, the S0/S1 sudoers lines and a seeded store. Or restate both checks
  as a CI job that uses a systemd-in-container image.

### OPS4-14 — MINOR — P10 (and the `deploy` template's post-check) call `/api/v1/system/version`, which no route serves

- **Spec:** 09 §9.7 P10 (line 870: `curl -s <abo-url>/api/v1/system/version`).
- **Evidence:**
  - The system routes are status, announcements, storage, logs, activity-log, reset and factory-reset
    (`internal/server/wire_system_routes.go:28-48`). No Go file at the evidence commit registers
    `system/version`.
  - `Makefile.local.example:103` uses the same path, so its "deployed version matches" step can never pass.
- **Fix:**
  - P10 uses `ssh <host-a> '<bin> --version-json'` (S0 on).
  - S0 adds `version` to `/health`, which is permanent, and the template's check reads it there.
  - Record the template defect under "Out-of-scope defects found".

### OPS4-15 — MINOR — S8 says a restore rewrites "both sidecars"; there are three

- **Spec:** 09 S8 "Backup" (line 728: "applying a restore rewrites both sidecars from the restored stamps") vs
  05-backup §5B.2 table (line 94: `.storage-levels`, `.storage-applied` and `.storage-format`).
- **Evidence:** `.storage-applied` is the updater's stepping-stone input (05-updater §5U.4 step 3). A text that
  omits it invites an implementation that leaves it stale until the next boot's step 9.
- **Fix:** "rewrites the three sidecars (`.storage-levels`, `.storage-applied`, `.storage-format`) from the
  restored store".

### OPS4-16 — MINOR — The stage that ships `--print-upgrade-floor` is unspecified, but every rollback runs it on `.prev`

- **Spec:** 05-storage §5S.3.7 reader check (lines 960-962: it runs `exe --print-upgrade-floor`; "a pre-guard
  binary declares none"); 09 §9.6 row 20 (line 835: lists the flag with no stage); 09 S0 (it ships
  `--print-storage-levels` and `--version-json` only).
- **Evidence:**
  - A guard-aware build without the flag reaches cobra and fails with "unknown flag", exit 1, empty stdout
    (`main.go:40-72`).
  - The spec defines "declares none" only for **pre-guard** binaries. A guard-aware S0–S4 `.prev` is
    therefore either `cannot_determine`, which refuses, or an unspecified special case.
- **Fix:**
  - S0 ships `--print-upgrade-floor`, printing an empty requirement.
  - The script treats "unknown flag" with empty stdout as "none" only when `--print-storage-levels` also
    fails (pre-guard).
  - Add the case to `scripts/test_storage_format_guard.py`.

### OPS4-17 — MINOR — OPS3-12 residue: semver comparison does not order `git describe` builds, so a main build can still be offered an older release as "newer"

- **Spec:** 09 S0 "Updater hotfix" (line 566: "compares versions with `golang.org/x/mod/semver` and refuses a
  target that is not newer … so a main build would be offered a 'downgrade'"); 05-updater §5U.5 "Downgrades:
  never automatic".
- **Evidence:**
  - Main builds take `VERSION := git describe --tags --always --dirty` (`Makefile:9`), for example
    `v0.223.0-5-gabc1234`.
  - Semver reads `-5-gabc1234` as a prerelease, so `v0.223.0-5-gabc1234` < `v0.223.0`.
  - A host that runs a main build five commits after the `v0.223.0` tag (before the next RC tag is fetched)
    is told that `releases/latest` = `v0.223.0` is newer, and a manual apply downgrades it.
- **Fix:**
  - A current version that carries a describe suffix (`-<n>-g<hex>`, or `-dirty`) counts as a local build.
    The check reports "local build, not compared" and apply refuses unless the operator names the version
    explicitly.
  - Add the test case "describe-suffixed current, older-or-equal base release → no update offered".
