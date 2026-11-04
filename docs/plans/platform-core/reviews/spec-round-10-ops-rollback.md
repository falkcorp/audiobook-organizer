<!-- file: docs/plans/platform-core/reviews/spec-round-10-ops-rollback.md -->
<!-- version: 1.0.0 -->
<!-- guid: e5a2fe0f-4dc9-40b3-938f-e3eb42c7cbca -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 10 — ops, rollback and incremental delivery

Lens: OPS, ROLLBACK AND INCREMENTAL DELIVERY. ID prefix `OPS10`. Spec under review: 09 v1.10.1, cross-checked
against 05-storage, 05-backup, 05-updater, 05-health, 14 and README. Code anchors are at `748fd787a`. Line numbers
are for 09 v1.10.1 unless another file is named.

**Totals: 11 findings — 0 BLOCKER, 6 MAJOR, 5 MINOR.**

I walked S0–S10 with three questions for each stage: can it ship, can its done-checks be measured somewhere, and
does §9.2.1 list its rollback cost. Every stage still ships and still rolls back by a binary swap. The 11-row table,
rows 10 and 11, the 60 s stop timeout and the `binary_id` staging check landed. The open defects sit in the
**soak gate**, which now decides when every stage may start:

- the trailer reaches only four stages (OPS10-1);
- the flip of S6–S8 can reach the next stage with no soak at all (OPS10-2);
- three of the pass criteria contradict the records they read, and one of them deadlocks S2b (OPS10-3);
- `<since>` comes from a boot log line that no stage emits (OPS10-4);
- revert admission is keyed on the commit message alone, and re-soaking is not enforced (OPS10-5).

Separately, 05-updater still specifies the rebuilt updater that A-6 deleted, so the S8 "move" is not one (OPS10-6).

## A. Regression check (round-9 BLOCKER/MAJOR in this lens, plus the items the lead asked about)

| ID | Round-9 issue | Status in round 10 |
|---|---|---|
| OPS9-1 (MAJOR) | Next-release content merges before the soak | **Half-fixed.** The `Stage-Release:` trailer and check (b) exist (09:202-206, :224-230). But the trailer has values for S2, S2b, S4, S5 and `floor` only, and nothing makes it present (OPS10-1). For S6–S8 the flip PR does not change `release:`, so the flip is never soaked (OPS10-2). |
| OPS9-2 (MAJOR) | Soak row not bound to code; no pass criteria | **Landed with defects.** Rows carry `binary_id` and `commit`, and the binary is verified (09:208-222). The criteria contradict their sources (OPS10-3), and `<since>` has no defined source (OPS10-4). |
| OPS9-3 (MAJOR) | Revert of a flip is refused | **Half-fixed.** Reverts are admitted (09:231-234), but only by the commit message. "Soaks afresh when re-landed" is not enforced (OPS10-5). |
| OPS9-4 (MAJOR) | S1 staged restore missing from §9.2.1 | **Fixed**: row 10, its release note, the S1 overview and the S1 Rollback row, and the R-test cell. A small residue is in OPS10-10. |
| OPS9-5 (MAJOR) | S4 r2 restore disables keys, with no note | **Fixed**: row 11, 09:158-160 rewritten, the S4 r2 release note. The row's range omits S8+ (OPS10-10). |
| OPS9-6 (MAJOR) | Done-checks with no environment | **Fixed in substance**: `unit-lifecycle` (09:251), and P7 branch B as an owner checkpoint (09:236-237, P7). Its triggers miss the PRs that change the checked behaviour, and the container cannot run the unit as committed (OPS10-7). |
| OPS9-8 (MAJOR) | Staging binary cannot be identified | **Fixed**: the S0 sidecar `<archive>.manifest.json` (05-backup B7) and the `binary_id` refusal (05-storage staging-marker row). The operation is impractical, though (OPS10-9). |
| CI gates list | `vet-darwin`, `vet-integration` | **Landed** (09:250, S0 table). `vet-darwin` with `-tags embed_frontend` cannot compile on a clean checkout (OPS10-8). |
| 60 s stop timeout | OPS9-11 | **Fixed** and consistent in 09 S2 (:1107) and 05-health 5H.4 (:511-517). HEAD's phases sum to about 45.5 s plus store close (05-health:45), so 60 s covers them. |
| §9.2.1 11 rows | OPS9-4/5 | **Fixed.** The 11 rows, "eleven rows" (09:47), the §9.3 overview cells, 14 K13 and the R-test list all agree. |
| S8 updater move | OPS9-17 / A-6 | **Not landed in 05-updater** (OPS10-6). |

## B. Findings

| ID | Sev | Title |
|---|---|---|
| OPS10-1 | MAJOR | OPS9-1 half-fixed: the trailer has no value for single-release stages, and nothing makes it present |
| OPS10-2 | MAJOR | For S6–S8, check (a) demands the stage's own soak row before its flip, and the next stage can then start on a row that predates the flip |
| OPS10-3 | MAJOR | The soak pass criteria contradict the records they read: S2b deadlocks, S4 r1 deadlocks on a documented remedy, and the health allow-list fails every soak from S3 |
| OPS10-4 | MAJOR | `soak_record.py` cannot compute `<since>` as specified, and has no defined place to run |
| OPS10-5 | MAJOR | OPS9-3 half-fixed: revert admission is decided by the commit message alone, and "re-landed releases soak afresh" is not enforced |
| OPS10-6 | MAJOR | The S8 updater "move" is still the rebuilt pipeline in 05-updater §5U.4 (archive, checksums, pre-update backup, container refusal) |
| OPS10-7 | MINOR | `unit-lifecycle` misses the PRs that change the behaviour it checks, and the committed unit is unlikely to start in the container as specified |
| OPS10-8 | MINOR | `vet-darwin` with `-tags embed_frontend` fails on every checkout without a built `web/dist` |
| OPS10-9 | MINOR | The staging writer binary is usually unavailable: only the current binary and `.prev` exist; the sidecar is not transported or pruned; the architecture is unstated |
| OPS10-10 | MINOR | Residues in rows 10 and 11: an S1→S0 override leaves an intent that blocks the S0 updater, and row 11 omits restores of pre-S8 archives on S8+ |
| OPS10-11 | MINOR | Cross-file drift on the soak gate (README, 14 K20) and one dangling reference |

### OPS10-1 — MAJOR — OPS9-1 half-fixed: the trailer has no value for single-release stages, and nothing makes it present

- **Spec.**
  - 09:202-206: "Every PR that ships behaviour for a **multi-release** stage carries `Stage-Release: <stage> <n>` …
    `<stage>` is `S2`, `S2b`, `S4` or `S5` … or `floor`".
  - A PR that changes `core/**` or `internal/books/**` and ships no stage behaviour says `none`.
  - "The PR template carries the line … The trailer is the author's declaration, checked in review."
  - Check (b) (09:227-229) refuses only a trailer that is present.
- **Evidence.**
  - **No value exists for S1, S3, S6, S7, S8, S9 or S10.** Some of their PRs ship live behaviour and do not change
    `release:`:
    - S1's staged restore (row 10, user-visible; S1 "ABO-local fixes" :1092);
    - S6's `c/outbox/` PR (:1195);
    - S7's `c/integrations/instances/` PR (:1209);
    - S8's backup-catalogue PR (:1223).

    Only "its first live PR" / "its first PR" sets `release:`, and no tool knows which PR is first.
  - So the `c/outbox/` PR, OPS9-1's own example, can merge while `release:` still reads `floor`. It then
    auto-promotes (`.github/workflows/prerelease.yml:7-13`) before the floor release has soaked. Its author must
    write either `none`, which is false, or no trailer.
  - **Presence is not enforced.** The PR template line lands in the PR body, not in commit messages. With rebase
    merges, check (b) reads commits, so filling in the template sets nothing.
  - 09:1181 assigns T2–T7 to `/parallel-sweep`, whose children write their own commits. 14 Q-I records that the
    sweep merges with `--admin`. "Checked in review" is therefore no check for the very batches the trailer was
    added for.
- **Fix.**
  - In 09 §9.2.1, change the trailer to `Stage-Release: <release-id>`, a value of the closed grammar (`S1`,
    `S2r2`, `S5r3`, `S6`, `floor`, …), or `none`. Every stage then has a value.
  - Add to check_soak.py: **(d)** refuse a PR that touches any non-doc path outside `ci/soak.txt` and
    `ci/prev_ref.txt` when one of its commits lacks a `Stage-Release:` trailer.
  - Apply check (b) unchanged to every value.
  - Ship a `commit-msg` hook or `.gitmessage` in S0 (not only a PR-template line), and require the
    `/parallel-sweep` task spec to stamp the trailer on every child commit.
  - Add `test_check_soak.py` cases for a missing trailer, an S6 content PR before the floor row, and an S1 content
    PR before the S0 row.
  - Update 14 K20, the 14 hazard row :91 and 03 §3.5.1 item 4 to match.

### OPS10-2 — MAJOR — For S6–S8, check (a) demands the stage's own soak row before its flip, and the next stage can then start on a row that predates the flip

- **Spec.**
  - 09:224-227, check (a): a PR that changes `release:` **or** `shipped_stage:` needs a row "for the predecessor
    release (**the one `release:` named before the PR**)".
  - S6, S7 and S8 (:1195, :1209, :1223): "its first live PR sets `release:` to `S6`". The flip PR sets only
    `shipped_stage:`.
  - 09:198-199: "a flip PR of a stage that lands live PR by PR may land inside a release whose first PR already
    changed it."
- **Evidence.** For the S6 flip PR (it deletes `registry.EventHub`, hub A and the v3 broker), the release named
  before the PR is `S6` itself. Check (a) therefore refuses the flip until `S6` has a 14-day row. Then:
  1. `soak_record.py` certifies `S6` on binaries **without** the flip. It checks only that the binary's
     `ci/stage.txt` names `release: S6` (09:212-213), which is true from the first live PR on.
  2. Once that row exists, the flip merges, and S7's first live PR is admitted the same day by the same pre-flip
     row.

  So the riskiest PR of S6, S7 and S8 (deleting `internal/plugin`; mounting the kernel's `/metrics` and deleting
  the staging code) reaches the next stage with zero days of soak. A revert of such a flip (OPS10-5) moves only
  `shipped_stage:` back. `release:` stays `S6`, and the soak keeps certifying a release whose flip was backed out.
- **Fix.** Prefer (1).
  1. Make the **flip PR** the PR that sets `release:` for S6–S8, as for every other stage. The earlier live PRs
     carry `Stage-Release: S6` (OPS10-1) and are gated by check (b) on the floor row. Delete the "may land inside a
     release" clause (09:198-199).
  2. Alternatively, define check (a) for a `shipped_stage:`-only change as needing no new row. Make
     `soak_record.py` count only boots of binaries whose `ci/stage.txt` carries that stage's `shipped_stage:`
     value, so `<since>` starts at the first post-flip boot.

  Either way, add `test_check_soak.py` cases for an S6 flip and an S7 first-live PR.

### OPS10-3 — MAJOR — The soak pass criteria contradict the records they read: S2b deadlocks, S4 r1 deadlocks on a documented remedy, and the health allow-list fails every soak from S3

- **Spec.**
  - 09:217-222: for `S2br1`/`S2br2`, "the persisted claim-violation record … **must be empty** (no violation, no
    refusal)".
  - For `S4r1`, the census "must list no undecodable row".
  - For every release, "no health token outside an allow-list (`auto_update_paused`), and no failed done-check in the
    stage's list".
  - S2b Releases (:1121) repeats "the empty persisted claim-violation record".
- **Evidence.**
  - **(i) S2b deadlock.** 05-storage §5S.9.4 (:2777-2784) says shadow and strict violations are persisted and
    "Entries are **kept until a strict-mode release has run 14 days** with none newer". S2b's own done-check reads
    `last_at` ("lists no writer for ≥ 14 days", :1126), and 14:81 agrees.
    - Shadow mode exists to find violations. Once one is found and fixed (:1121, "every violation is fixed by
      registering or correcting the writer"), its entry stays.
    - `S2br1` can then never be "empty", `S2br2` (strict) is refused for ever, and S3 waits on `S2br2`'s row.
    - The deadlock is broken only by strict mode, which is the thing it blocks.
  - **(ii) S4 r1.** P1 branch B (:1361) lets the owner have undecodable rows "fixed **or restricted** before r2".
    §9.2.3 rule 3 keeps a restricted row undecodable and counted, so the restricted remedy never passes the census
    criterion.
  - **(iii) Health allow-list.** The spec's own advisory tokens are expected on a healthy reference host:
    - `legacy_config_keys` for the whole of W from S3 (09:578). A host that follows row 3's advice ("keep the stored
      setting, or its old variable, … while you may roll back") keeps exactly these names.
    - `config_legacy_name_shadowed` and `auth_disabled_remote` (05-health:153-166).
    - Per-instance `core.integrations.*` Degraded while a provider is down, and `core.outbox` Degraded "while any
      dead letter exists" (05-health:140-147).

    So a host that follows row 3 can never record a soak from S3 onward. Moving it to canonical names to pass the
    soak loses the setting on rollback (row 3).
  - **(iv)** `soak_record.py` has no way to know whether "a done-check in the stage's list" failed. Most of them are
    CI tests or owner checkpoints.
- **Fix.**
  - (i) In 09:217-218 and S2b :1121, replace "empty" with "no entry in that release's mode whose `last_at` is at or
    after `<since>`", the same rule as the done-check and 05-storage. Leave 05-storage's retention unchanged.
  - (ii) Make the census criterion "no undecodable row that is not on the owner-acknowledged P1 list committed in
    the stage record".
  - (iii) Make the health criterion a point-in-time read at record time. Require no Critical component Down and no
    token outside a committed allow-list `ci/soak_health_allow.txt` (initially `auto_update_paused`,
    `legacy_config_keys`, `config_legacy_name_shadowed`, `auth_disabled_remote`, `revocation_journal_colocated`, and
    `core.integrations.*` reasons). The file is owner-edited and checked as bookkeeping by check (c).
  - (iv) Drop "no failed done-check" from the script's criteria. Instead, require the stage's done-check list to
    be ticked in the stage record before the row PR, and keep that as an owner checkpoint.

### OPS10-4 — MAJOR — `soak_record.py` cannot compute `<since>` as specified, and has no defined place to run

- **Spec.**
  - 09:209-222: the owner runs `scripts/soak_record.py` "on the reference host".
  - It verifies the commit "on `main` … (`git merge-base --is-ancestor`)".
  - `<since>` is "the start of the oldest boot from which every boot **in the unit's journal** ran a binary of
    `<id>` (**each boot logs its `binary_id`**)".
  - It writes `<state_dir>/soak/<release>.json`.
- **Evidence.**
  - **No stage emits the boot line.** S0 "Endpoints" (:1045) adds `--version-json` and `/health`'s `version`, but no
    per-boot log of `binary_id`. A grep of the spec finds "each boot logs its `binary_id`" only at 09:214.
  - **Journal retention.** A 14-day window depends on journald keeping 14 days of a chatty unit. When the boot that
    opened the release has rotated out, `<since>` is undefined.
  - **No git on the host.** The reference host gets only an `scp`'d binary
    (`Makefile.local.example:88-110` @748fd787a: `make build-linux` on the operator's machine, then `scp` and
    `sudo mv`). It has no `main` history for `git merge-base`.
  - **Permissions.** The state directory belongs to the service user. Reading the unit journal needs the
    `systemd-journal` group or sudo. §9.2.2's S0 sudoers lines cover only the two sidecars and the restore-probe
    `find` (09:403-406). As written, the command needed to advance every stage has no environment, which is OPS9-6's
    defect class.
- **Fix.**
  - In 09 §9.2.1, run `soak_record.py` **from the operator's checkout over ssh**, like `make rollback`
    (`DEPLOY_HOST`, `DEPLOY_BIN`, `DEPLOY_STATE_DIR`). Do the git checks locally, and keep the evidence file in the
    checkout's scratch area, not in the state directory.
  - Add to S0 "Endpoints": every S0+ boot logs one INFO line `boot binary_id=<id> release=<release:>` before the
    store opens (`TestBootLogsBinaryID`).
  - State that the script refuses, saying "journal does not cover the soak", when the oldest retained journal entry
    is newer than the candidate `<since>`. Add a P-row that checks the journald retention (`journalctl --disk-usage`,
    the oldest entry for the unit) before S1.
  - Add the read-only `journalctl -u <unit>` sudoers line to the S0 owner actions and to the preflight's
    `sudo -n -l` check.

### OPS10-5 — MAJOR — OPS9-3 half-fixed: revert admission is decided by the commit message alone, and "re-landed releases soak afresh" is not enforced

- **Spec.**
  - 09:231-235: a PR "whose commit subject starts with `Revert` and whose message names, in git's 'This reverts
    commit <sha>' line, a reverted commit that carries a `Stage-Release:` trailer or changed `shipped_stage:`
    **passes with no soak row**".
  - "A reverted release soaks afresh when it is re-landed; its old row no longer counts."
  - The ledger holds "one row per stage release" (09:208). §9.8 row (:1411) repeats the rule.
- **Evidence.**
  - **Message-only test.**
    - A PR with one such commit plus any other commits, or one whose "revert" commit also edits other files, is
      admitted with no soak.
    - "Passes" does not say that check (c) (grammar, `ci/soak.txt` alone) still applies.
    - Every trailered content commit counts as a "flip" for this test.
  - **Re-soak unenforced.** Suppose release N soaked, got its row, and N's flip was then reverted and later
    re-landed. N+1's first PR is checked by (a): "a row for the predecessor … whose `commit` is an ancestor of the
    PR's base with a `ci/stage.txt` naming that predecessor". The old pre-revert row satisfies every clause, so the
    re-landed code never soaks.
  - The one-row-per-release ledger also has no place for a second landing's row.
- **Fix.**
  - In 09 §9.2.1, admit a backwards change only when **every** commit of the PR is a `git revert` of a named
    commit, and the PR's tree equals the base with those reverts applied. `check_soak.py` recomputes
    `git revert --no-commit <sha>…` and compares trees. Check (c) still applies.
  - Key rows as `<release> <landing commit> …`, where the landing commit is the latest first-parent commit on `main`
    that set `release:` to that ID.
  - Checks (a) and (b) accept only a row whose landing commit equals the current one for that ID.
    `soak_record.py` starts `<since>` no earlier than that commit's first boot.
  - Add `test_check_soak.py` cases: a revert PR with an extra commit (refused); a re-landed release with only its
    old row (refused).

### OPS10-6 — MAJOR — The S8 updater "move" is still the rebuilt pipeline in 05-updater §5U.4 (archive, checksums, pre-update backup, container refusal)

- **Spec.**
  - 09 S0 :1047: "S8 moves this updater into core **unchanged in behaviour** (A-6)".
  - 09 S8 :1234: "the moved updater passes S0's updater test unchanged (fake release server … HEAD asset name)", and
    `TestABOUpdaterSourceMatchesHEAD` ("asset name per GOOS/GOARCH" equal to HEAD's).
  - 05-updater 5U.1 (:22-31) says "moves; it does not rebuild". Its normative apply steps (§5U.4, :306-348) are
    still the rebuilt pipeline:
    - step 1 "not in a container";
    - step 2 "Download **the archive**, and `checksums.txt`";
    - step 4 "**Extract** only the `BinaryName` member";
    - step 6 "**Pre-update backup** through `backup.Runner` … failure aborts unless
      `core.updater.require_backup=false`" (default `true`, :450);
    - plus `AssetName` defaulting to `GoreleaserArchive` (:69), `.failed` and `restart_mode=exec`;
    - tests `TestApply_BackupFailureAborts`, `TestApply_ArchiveMemberTraversalRejected` and
      `TestPolicy_ContainerForcesOff` (:500-514).
- **Evidence.**
  - HEAD downloads a **raw** asset `audiobook-organizer-<GOOS>-<GOARCH>` and writes it straight to `<exe>.new`
    (`internal/updater/updater.go:214-265,318-326` @748fd787a). HEAD's updater has no backup, checksum or container
    step.
  - By 09's own precedence (:12-14, "on *how* a mechanism works, the owning 05-* file wins"), S8 ships 05-updater's
    pipeline:
    - a manual apply on the reference host first writes a full archive (archives there are about 15 GB each,
      `internal/backup/backup.go` retention comment) and aborts when that fails;
    - retention may delete older archives to make room for it;
    - step 4 cannot extract a member from HEAD's raw asset, so either `TestABOUpdaterSourceMatchesHEAD` or "S0's test
      unchanged" fails;
    - none of these behaviour changes has an S8 release note.
- **Fix.**
  - In 05-updater §5U.4, restate steps 1–7 as the S0 hotfix:
    - download HEAD's raw asset (`AssetName` returns it for ABO), with a size cap;
    - use checksums only when `checksums.txt` names the raw asset;
    - make archive extraction, the pre-update backup (`require_backup`, `backup.Runner`) and `restart_mode=exec`
      deferred-build members in 5U.3's list;
    - remove or move the matching tests.
  - Keep the container refusal only with an S8 "User-visible changes" line in 09 ("in a container, update checks
    report `container` and apply is refused") and its release note.
  - Add to the S8 done-check: an apply takes no backup and fetches the raw asset.

### OPS10-7 — MINOR — `unit-lifecycle` misses the PRs that change the behaviour it checks, and the committed unit is unlikely to start in the container as specified

- **Spec.** 09:251 and the S0 table: triggers are "every flip PR, every PR that changes `deploy/**`, and nightly".
  The drop-in "overrides only `ExecStart` and `DATABASE_PATH`", in a systemd-as-PID-1 container with
  `--network none`.
- **Evidence.**
  - **Triggers.**
    - S0 and S1 have no flip PR (:1035, :1090).
    - S1's staged-restore PR touches `internal/backup`, not `deploy/**`.
    - S2b's reset PR is a separate, non-flip PR (:1122).

    So the S0, S1 and S2b unit checks run only nightly, and nothing says that nightly gates. The PR that introduces
    exit-75 restore or reset staging merges without them.
  - **Container.** The committed unit sets `User=audiobook`, `WorkingDirectory=/var/lib/audiobook-organizer`,
    `MemoryMax=12G`, `ProtectKernelTunables=yes`, `ProtectControlGroups=yes` and `PrivateTmp=yes`
    (`deploy/audiobook-organizer.service:81-125` @748fd787a). A systemd container needs a cgroup namespace or
    `--privileged`, a tmpfs `/run`, and an image that creates the user and directories. The sandboxing directives
    commonly fail there with 226/NAMESPACE, and the drop-in may not touch them.
- **Fix.**
  - Trigger `unit-lifecycle` also on any PR that touches `cmd/**`, `internal/backup/**`, `internal/updater/**`,
    `internal/server/server_lifecycle*.go`, `core/kernel/**` or `core/internal/restoreswap/**`.
  - Or, simpler, run it on GitHub's Ubuntu runner VM, which has systemd as PID 1, with a drop-in that adds
    `PrivateNetwork=yes`. The fixture store needs no `--network none` container.
  - Either way, state the privileges and image preparation.

### OPS10-8 — MINOR — `vet-darwin` with `-tags embed_frontend` fails on every checkout without a built `web/dist`

- **Spec.** 09:250 and the S0 table: `GOOS=darwin CGO_ENABLED=0 go vet -tags embed_frontend ./...` over the root
  and `core`. 03 §3.5.1 item 4 also puts it in root `make ci`.
- **Evidence.** `web_embed.go` (`//go:build embed_frontend`, `//go:embed web/dist`) fails to compile when `web/dist`
  is absent. `ci.yml` builds no frontend in its Go jobs (no `web/dist` step @748fd787a). Every fresh worktree lacks
  it (the `npm ci --prefix web` recipe does not build). Core's hello `web/dist` has the same problem. As written, the
  required check, and root `make ci` for `/parallel-sweep`, is red on every PR from S0.
- **Fix.** The job and the make target create the listed in-image outputs as placeholders
  (`mkdir -p web/dist && : > web/dist/index.html`, same for hello's dist, taken from
  `scripts/ci/embed_build_outputs.txt`) before vetting. Alternatively, depend on `make web-build` and count it in
  §9.2.10's budget.

### OPS10-9 — MINOR — The staging writer binary is usually unavailable: only the current binary and `.prev` exist; the sidecar is not transported or pruned; the architecture is unstated

- **Spec.**
  - 09:974-976: "`binary_id` hashes the executable, so `provision.sh` copies that binary read-only from the
    reference host (its current binary or `.prev`) … and stops when neither matches".
  - 05-backup B7: the sidecar "which no older build lists or prunes".
- **Evidence.**
  - The owner deploys `main` builds often (P10, "deploys build main"), so a scheduled archive is stageable only
    until the second deploy after it. After that, every staging run needs a fresh full production archive.
  - HEAD's retention and `DeleteBackupIn` remove only `.sha256` (`internal/backup/backup.go:1006-1021,1257-1268`
    @748fd787a). Nothing says S0–S7's `internal/backup` removes `.manifest.json`, so orphans accumulate until S8's
    reconciliation (05-backup:828-831).
  - An archive fetched through the download route arrives without its sidecar, and `staging-restore` then refuses
    it.
  - The binary is linux/amd64, and the `deploy` recipe cross-compiles from macOS. Nothing says the staging container
    runs on matching hardware. Under emulation the boot-budget ceilings are meaningless.
- **Fix.**
  - The `deploy` recipe keeps the last N deployed binaries under `<bin>.d/<binary_id>` (bounded N), and
    `provision.sh` fetches by `binary_id`.
  - S0's `internal/backup` removes `<archive>.manifest.json` wherever it removes `.sha256`.
  - `provision.sh` copies archive and sidecar together.
  - §9.3 states that staging runs on a linux/amd64 host.

### OPS10-10 — MINOR — Residues in rows 10 and 11: an S1→S0 override leaves an intent that blocks the S0 updater, and row 11 omits restores of pre-S8 archives on S8+

- **Evidence.**
  - **Row 10 (:155).** After `make rollback` from S1 to S0 with `ROLLBACK_IGNORE_RESTORE=1`, S0 does not consume
    `restore/pending.json`. Its updater stats `restore/` and refuses every manual apply, with no override (§9.2.2,
    :396-398). The roll-forward `deploy` refuses again until the override is repeated. Neither the row nor its
    release note says so.
  - **Row 11 (:156).** The row says "a staged restore of an ABO-format archive (S4 r2–S7)". But an archive made
    before S8 is still `legacy-abo/0` with nil coverage when restored on S8+ (05-backup B7; 14 K22 (d)), and the S8
    overview cell (:894) says "—".
- **Fix.**
  - Row 10: add "after a rollback to S0 with the override, the staged intent stays until the next S1+ boot rejects
    it; repeat the override for the next deploy; the S0 updater refuses until then".
  - Row 11: "on S4 r2 or later, a staged restore of any archive made before S8 …".
  - S8 overview: "row 11 (pre-S8 archives)".

### OPS10-11 — MINOR — Cross-file drift on the soak gate (README, 14 K20) and one dangling reference

- **Evidence.**
  - README:42 says the soak ledger "gat[es] **every** PR through a `Stage-Release:` trailer". 09:202-206 limits it
    to multi-release stages (see OPS10-1).
  - 14 K20 says the binary is "at or after **the stage's** first PR"; 09:212-213 says "the **release's** first PR".
    K20 also says check (b) refuses a trailer "as it refuses a change of the `shipped_stage:` or `release:` line …
    without one", which misreads 09 (the marker change needs a row, not a trailer).
  - 05-config:2341 cites "09 §9.4 row 17", but §9.4 has no numbered rows.
- **Fix.** Align README:42 and 14 K20 with 09 §9.2.1 (after OPS10-1 and OPS10-5), using "release's first PR". Point
  05-config:2341 at 09 S3 or §9.4.1's S3 row.
