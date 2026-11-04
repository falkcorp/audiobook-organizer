<!-- file: docs/agent-tasks/platform-core/OWNER-15-release-s5r3-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: 127baae8-92e4-4395-8a2b-494bc0a4f8f6 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-15 — Release S5r3: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> ## NOT AGENT WORK
> This is an owner checkpoint. No agent may perform, tick or complete any step below: no agent deploys to the
> reference host, runs `scripts/soak_record.py` (either mode), runs a production read of 09 §9.7, edits `ci/soak.txt`,
> `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, ticks the stage record, or merges a PR that changes those files
> (09 §9.2.1 "Soaks and owner checkpoints"; plan §(g)). An agent handed this file stops and returns it to the owner.

| Field | Value |
|---|---|
| ID | OWNER-15 |
| Tier | OWNER — NOT AGENT WORK |
| Wave | W13 (stage S5, release `S5r3` = T4 + T5) |
| Depends on | PC-13-03, PC-13-04, PC-13-05, PC-13-06 (all merged); predecessor row `S5r2` current in `ci/soak.txt` |
| Release ID | `S5r3` (09 §9.2.1 grammar `S5r[1-9][0-9]?`) |
| Duration | at least 14 days of soak on the deploy that carries the release's last PR (the last of PC-13-04, PC-13-05, PC-13-06 to merge) |
| Unblocks | W13's `S5r4` (PC-13-07, which changes `release:` to `S5r4`) |
| Spec | 09 §9.2.1 "Soaks and owner checkpoints" and rollback row 5; 09 §9.3 boot-time budget and staging; 09 §9.7 (P10); 09 S5 "Done when"; 05-tasks §5T.8.2 row(s) T4, T5; digest `docs/plans/platform-core/digests/S5.md` §6-§8 |

## Preconditions

- `ci/soak.txt` holds a current `S5r2` row (OWNER-14).
- PC-13-03, PC-13-04, PC-13-05, PC-13-06 have merged; `ci/stage.txt` on `main` reads `shipped_stage: S5` and `release: S5r3` (set by PC-13-03).
- `compat-rehearsal` passed on PC-13-03 (09 §9.2.1 required checks: every PR that changes `release:`).

## Owner steps

1. **Release notes** for the production release that first contains `S5r3`, prepended to the generated body (never
   replacing it): 09 §9.2.1 row 5 for the definitions whose loop moved to a `Watermark` checkpoint and for the iTunes import and
   `dedup.drain-stale` (they restart from zero once after a rollback; the PR descriptions of PC-13-04 to PC-13-06 list
   them); one line for every T4 kind now owned by core, if PC-13-03 converted any.
2. **Boot-budget A/B** for `S5r3` on the scrubbed production-size staging copy (09 §9.3): `scripts/boot_budget.py
   --prev <bin> --cur <bin> --db-copy <dir> --runs 5`; steady state must meet p50(cur) ≤ max(1.10 × p50(prev),
   p50(prev) + 15 s); append the result to the stage record.
3. **Deploy** the build that carries the last of PC-13-04, PC-13-05, PC-13-06 to merge to the reference host with the `deploy` recipe; it runs
   `scripts/soak_record.py --deployed` after its ready check (run it by hand after any other deploy). A later deploy
   of a `S5r3` binary keeps the clock; a `make rollback` or another release's binary restarts it.
4. **P10 after each deploy** (09 §9.7): read `<bin>.prev --version-json` and `<bin> --version-json` over ssh, and
   commit the resolved `PREV_REF` as `ci/prev_ref.txt` in a bookkeeping-only PR.
5. **Watch the soak**: `/health` stays `ok` apart from tokens in `ci/soak_health_allow.txt`; no unit exit other than
   0 or 75; no `restore_*` token. After the first deploy run `acoustid.backfill` and `dedup.full-scan` once and confirm their progress and worker
   counts match the previous release (CLAUDE.md concurrency: T5 changes no parallelism).
6. **Tick the stage record's done-check list** for this release (`docs/plans/platform-core/stage-ledger/S5.md`):
   - T4: `TestLegacyTaskAliases` extended to one T4 kind; the op-ID ledger green; `TestCoreHoldsNoAppKeyNames` green;
   - T5: the op-ID ledger green; declared Parallelism equals the legacy Concurrency (1:1) per the PR tables of
     PC-13-04 and PC-13-05; the T5 plugin packages import no `pkg/plugin/sdk` (see Planner notes);
   - the per-stage boot budget (step 2) and the R-test cells of `compat-rehearsal` on PC-13-03 (09 §9.3).
7. **Write the row**: once the deploy carrying the last of PC-13-04, PC-13-05, PC-13-06 to merge has served 14 days, run `scripts/soak_record.py --release
   S5r3` from the operator's checkout over ssh (`DEPLOY_HOST`, `DEPLOY_BIN`, `DEPLOY_STATE_DIR`). It prints the row
   `<release> <landing commit> <binary_id> <commit> <since, UTC> <levels> <evidence SHA-256>` only when every
   criterion passes (zero `restore_*` and boot errors since `<since>`; every owner at level 0 in both
   `--print-storage-levels` and `<db path>.storage-levels`; one health read with no Critical component Down and no
   token outside the allow-list), and refuses with "journal does not cover the soak" when the unit's journal is too
   short (P15). Merge a **bookkeeping-only** PR that adds only that row to `ci/soak.txt`.

## Done when

- `ci/soak.txt` holds a current `S5r3` row written by `scripts/soak_record.py --release S5r3`;
- the stage record ticks the stage's done-check list for `S5r3`;
- P10 ran after each deploy and `ci/prev_ref.txt` is updated.

## Never

- Let an agent run any step of this file, or merge the bookkeeping PR.
- Print session, secret or token values; counts only (09 §9.7).

## Planner notes

- PC-13-03 is SPEC-GATED (no T4 kind list in 04). It is also the PR that sets `release: S5r3`, and PC-13-04 to
  PC-13-06 depend on it, so while it is gated `S5r3` cannot land. Either a spec edit unblocks it, or the plan moves the
  `release: S5r3` change (and the dependency) to PC-13-04 and lets T4 follow in a later release.
- The gate "`pkg/plugin/sdk` unused" (05-tasks §5T.8.2 T5) cannot hold at `S5r3`: the maintenance plugin (T6) and
  `internal/scheduler` (T7) still import it. Tick only the T5 packages.
