<!-- file: docs/agent-tasks/platform-core/OWNER-17-release-s5r5-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: d37d1f84-f733-4358-86dd-155fc1a91940 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-17 — Release S5r5: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> ## NOT AGENT WORK
> This is an owner checkpoint. No agent may perform, tick or complete any step below: no agent deploys to the
> reference host, runs `scripts/soak_record.py` (either mode), runs a production read of 09 §9.7, edits `ci/soak.txt`,
> `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, ticks the stage record, or merges a PR that changes those files
> (09 §9.2.1 "Soaks and owner checkpoints"; plan §(g)). An agent handed this file stops and returns it to the owner.

| Field | Value |
|---|---|
| ID | OWNER-17 |
| Tier | OWNER — NOT AGENT WORK |
| Wave | W13 (stage S5, release `S5r5` = T7) |
| Depends on | PC-13-14, PC-13-15 (all merged); predecessor row `S5r4` current in `ci/soak.txt` |
| Release ID | `S5r5` (09 §9.2.1 grammar `S5r[1-9][0-9]?`) |
| Duration | at least 14 days of soak on the deploy that carries the release's last PR (PC-13-15) |
| Unblocks | W14 (PC-14-01, the floor release) |
| Spec | 09 §9.2.1 "Soaks and owner checkpoints" and rollback row 5; 09 §9.3 boot-time budget and staging; 09 §9.7 (P10, P13); 09 S5 "Done when"; 05-tasks §5T.8.2 row(s) T7; digest `docs/plans/platform-core/digests/S5.md` §6-§8 |

## Preconditions

- `ci/soak.txt` holds a current `S5r4` row (OWNER-16).
- PC-13-14, PC-13-15 have merged; `ci/stage.txt` on `main` reads `shipped_stage: S5` and `release: S5r5` (set by PC-13-14).
- `compat-rehearsal` passed on PC-13-14 (09 §9.2.1 required checks: every PR that changes `release:`).

## Owner steps

1. **Release notes** for the production release that first contains `S5r5`, prepended to the generated body (never
   replacing it): T7 is a behaviour change: list every moved schedule key with its alias (the table in PC-13-14's PR), say the old
   `scheduled.*`/`maintenance.*` names keep working for the compatibility window, that the 15 never-run v2 cron
   declarations are deleted (14 Q-C), and 09 §9.2.1 row 5 for the two new kinds (full sweep, batch poll) and the bulk
   write-back restart. Add: "Schedule changes saved under the new key names are not seen after a rollback; the old
   names are."
2. **Boot-budget A/B** for `S5r5` on the scrubbed production-size staging copy (09 §9.3): `scripts/boot_budget.py
   --prev <bin> --cur <bin> --db-copy <dir> --runs 5`; steady state must meet p50(cur) ≤ max(1.10 × p50(prev),
   p50(prev) + 15 s); append the result to the stage record.
   Also run the **staging schedule smoke** (09 S5 "Done when"): boot the staging copy, check that the boot log lists
   the scheduler table (name, enabled, interval, next due) for all converted schedules, and that every enabled
   schedule with an interval of 24 h or less fires within one interval; list the rest with their next due time in the
   stage record.
3. **Deploy** the build that carries PC-13-15 to the reference host with the `deploy` recipe; it runs
   `scripts/soak_record.py --deployed` after its ready check (run it by hand after any other deploy). A later deploy
   of a `S5r5` binary keeps the clock; a `make rollback` or another release's binary restarts it.
4. **P10 after each deploy** (09 §9.7): read `<bin>.prev --version-json` and `<bin> --version-json` over ssh, and
   commit the resolved `PREV_REF` as `ci/prev_ref.txt` in a bookkeeping-only PR.
   **P13 for the floor release's stepping stone** (09 §9.7 P13, §9.2.2): read `ssh <host-a> 'sudo -n cat
   <db>.storage-applied'` now and record the value in the stage record. When PC-14-01's build exists, compare it with
   that build's `<new-bin> --print-upgrade-floor` (OWNER-18 repeats this before deploying `floor`): if the store is
   below the floor, the stepping-stone release must be installed first.
5. **Watch the soak**: `/health` stays `ok` apart from tokens in `ci/soak_health_allow.txt`; no unit exit other than
   0 or 75; no `restore_*` token. Check `GET /api/core/v1/tasks/schedules` lists the converted schedules and the Settings "Tasks" page shows the
   same enable flags and intervals as before the deploy; after the first maintenance-window night confirm the window
   members ran in order.
6. **Tick the stage record's done-check list** for this release (`docs/plans/platform-core/stage-ledger/S5.md`):
   - T7: `TestOneSchedulerPerKind`; `TestScheduleParity_AllDefinitions` (moved, golden unchanged);
     `TestABOMetricsGatherNoDuplicateOrMissing` (the v3 loop gone); `internal/scheduler` deleted;
     `TestLegacyOpsWriters` at its final target; no `registry.` import outside opsbridge;
   - the staging schedule smoke (step 2);
   - the per-stage boot budget (step 2) and the R-test cells of `compat-rehearsal` on PC-13-14, including "a schedule
     clock advanced on prev neither double-fires nor stampedes on cur" (09 S5 "Done when").
7. **Write the row**: once the deploy carrying PC-13-15 has served 14 days, run `scripts/soak_record.py --release
   S5r5` from the operator's checkout over ssh (`DEPLOY_HOST`, `DEPLOY_BIN`, `DEPLOY_STATE_DIR`). It prints the row
   `<release> <landing commit> <binary_id> <commit> <since, UTC> <levels> <evidence SHA-256>` only when every
   criterion passes (zero `restore_*` and boot errors since `<since>`; every owner at level 0 in both
   `--print-storage-levels` and `<db path>.storage-levels`; one health read with no Critical component Down and no
   token outside the allow-list), and refuses with "journal does not cover the soak" when the unit's journal is too
   short (P15). Merge a **bookkeeping-only** PR that adds only that row to `ci/soak.txt`.

## Done when

- `ci/soak.txt` holds a current `S5r5` row written by `scripts/soak_record.py --release S5r5`;
- the stage record ticks the stage's done-check list for `S5r5`;
- P10 ran after each deploy and `ci/prev_ref.txt` is updated.
- P13's reading of `<db>.storage-applied` is in the stage record for OWNER-18 and PC-14-01's stepping-stone choice.

## Never

- Let an agent run any step of this file, or merge the bookkeeping PR.
- Print session, secret or token values; counts only (09 §9.7).

## Planner notes

- The plan's note "Run P13 for the floor release's stepping stone" is honoured as a reading here and a comparison at
  OWNER-18, because the floor binary (PC-14-01) does not exist until after this row.
