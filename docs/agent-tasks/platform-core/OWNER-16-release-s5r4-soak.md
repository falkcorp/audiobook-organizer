<!-- file: docs/agent-tasks/platform-core/OWNER-16-release-s5r4-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9e0f5f56-fab0-4c39-8b99-c4f6611bde47 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-16 — Release S5r4: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> ## NOT AGENT WORK
> This is an owner checkpoint. No agent may perform, tick or complete any step below: no agent deploys to the
> reference host, runs `scripts/soak_record.py` (either mode), runs a production read of 09 §9.7, edits `ci/soak.txt`,
> `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, ticks the stage record, or merges a PR that changes those files
> (09 §9.2.1 "Soaks and owner checkpoints"; plan §(g)). An agent handed this file stops and returns it to the owner.

| Field | Value |
|---|---|
| ID | OWNER-16 |
| Tier | OWNER — NOT AGENT WORK |
| Wave | W13 (stage S5, release `S5r4` = T6) |
| Depends on | PC-13-07, PC-13-08, PC-13-09, PC-13-10, PC-13-11, PC-13-12, PC-13-13 (all merged); predecessor row `S5r3` current in `ci/soak.txt` |
| Release ID | `S5r4` (09 §9.2.1 grammar `S5r[1-9][0-9]?`) |
| Duration | at least 14 days of soak on the deploy that carries the release's last PR (the last of PC-13-08 to PC-13-13 to merge) |
| Unblocks | W13's `S5r5` (PC-13-14, which changes `release:` to `S5r5`) |
| Spec | 09 §9.2.1 "Soaks and owner checkpoints" and rollback row 5; 09 §9.3 boot-time budget and staging; 09 §9.7 (P10); 09 S5 "Done when"; 05-tasks §5T.8.2 row(s) T6; digest `docs/plans/platform-core/digests/S5.md` §6-§8 |

## Preconditions

- `ci/soak.txt` holds a current `S5r3` row (OWNER-15).
- PC-13-07, PC-13-08, PC-13-09, PC-13-10, PC-13-11, PC-13-12, PC-13-13 have merged; `ci/stage.txt` on `main` reads `shipped_stage: S5` and `release: S5r4` (set by PC-13-07).
- `compat-rehearsal` passed on PC-13-07 (09 §9.2.1 required checks: every PR that changes `release:`).

## Owner steps

1. **Release notes** for the production release that first contains `S5r4`, prepended to the generated body (never
   replacing it): 09 §9.2.1 row 5 for the maintenance-plugin definitions whose loop moved to a `Watermark` checkpoint (the seven PR
   descriptions list them).
2. **Boot-budget A/B** for `S5r4` on the scrubbed production-size staging copy (09 §9.3): `scripts/boot_budget.py
   --prev <bin> --cur <bin> --db-copy <dir> --runs 5`; steady state must meet p50(cur) ≤ max(1.10 × p50(prev),
   p50(prev) + 15 s); append the result to the stage record.
3. **Deploy** the build that carries the last of PC-13-08 to PC-13-13 to merge to the reference host with the `deploy` recipe; it runs
   `scripts/soak_record.py --deployed` after its ready check (run it by hand after any other deploy). A later deploy
   of a `S5r4` binary keeps the clock; a `make rollback` or another release's binary restarts it.
4. **P10 after each deploy** (09 §9.7): read `<bin>.prev --version-json` and `<bin> --version-json` over ssh, and
   commit the resolved `PREV_REF` as `ci/prev_ref.txt` in a bookkeeping-only PR.
5. **Watch the soak**: `/health` stays `ok` apart from tokens in `ci/soak_health_allow.txt`; no unit exit other than
   0 or 75; no `restore_*` token. After the first deploy read `<abo-url>/metrics` with an admin key and confirm
   `audiobook_organizer_ops_abandoned_calls` has exactly one series per `source` value (`repairs_resolver`,
   `retired_versions_stat`) and the scrape answers 200 (the owner change of PC-13-13; 05-metrics §13).
6. **Tick the stage record's done-check list** for this release (`docs/plans/platform-core/stage-ledger/S5.md`):
   - T6: the op-ID ledger green; declared Parallelism equals the legacy Concurrency (1:1) in all seven sub-batches
     (`TestT6Group<k>ParallelismOneToOne`); `TestT6GroupsCoverRegister` green; the maintenance plugin registers no v2 def;
   - `TestABOMetricsGatherNoDuplicateOrMissing` with both source values of `ops_abandoned_calls` (PC-13-13); the seeded
     series contract test unchanged;
   - the per-stage boot budget (step 2) and the R-test cells of `compat-rehearsal` on PC-13-07 (09 §9.3).
7. **Write the row**: once the deploy carrying the last of PC-13-08 to PC-13-13 to merge has served 14 days, run `scripts/soak_record.py --release
   S5r4` from the operator's checkout over ssh (`DEPLOY_HOST`, `DEPLOY_BIN`, `DEPLOY_STATE_DIR`). It prints the row
   `<release> <landing commit> <binary_id> <commit> <since, UTC> <levels> <evidence SHA-256>` only when every
   criterion passes (zero `restore_*` and boot errors since `<since>`; every owner at level 0 in both
   `--print-storage-levels` and `<db path>.storage-levels`; one health read with no Critical component Down and no
   token outside the allow-list), and refuses with "journal does not cover the soak" when the unit's journal is too
   short (P15). Merge a **bookkeeping-only** PR that adds only that row to `ci/soak.txt`.

## Done when

- `ci/soak.txt` holds a current `S5r4` row written by `scripts/soak_record.py --release S5r4`;
- the stage record ticks the stage's done-check list for `S5r4`;
- P10 ran after each deploy and `ci/prev_ref.txt` is updated.

## Never

- Let an agent run any step of this file, or merge the bookkeeping PR.
- Print session, secret or token values; counts only (09 §9.7).

## Planner notes

- If PC-13-07's grouping found fewer than seven groups, the empty sub-batches opened no PR; record which in the
  stage record so this checkpoint's dependency list is read as "every non-empty sub-batch merged".
