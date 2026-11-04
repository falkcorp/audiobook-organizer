<!-- file: docs/agent-tasks/platform-core/OWNER-14-release-s5r2-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: e28d0574-8aad-447d-ade1-55ed374b893b -->
<!-- last-edited: 2026-11-04 -->

# OWNER-14 — Release S5r2: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> ## NOT AGENT WORK
> This is an owner checkpoint. No agent may perform, tick or complete any step below: no agent deploys to the
> reference host, runs `scripts/soak_record.py` (either mode), runs a production read of 09 §9.7, edits `ci/soak.txt`,
> `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, ticks the stage record, or merges a PR that changes those files
> (09 §9.2.1 "Soaks and owner checkpoints"; plan §(g)). An agent handed this file stops and returns it to the owner.

| Field | Value |
|---|---|
| ID | OWNER-14 |
| Tier | OWNER — NOT AGENT WORK |
| Wave | W13 (stage S5, release `S5r2` = T2 + T3) |
| Depends on | PC-13-01, PC-13-02 (all merged); predecessor row `S5r1` current in `ci/soak.txt` |
| Release ID | `S5r2` (09 §9.2.1 grammar `S5r[1-9][0-9]?`) |
| Duration | at least 14 days of soak on the deploy that carries the release's last PR (PC-13-02) |
| Unblocks | W13's `S5r3` (PC-13-03, which changes `release:` to `S5r3`) |
| Spec | 09 §9.2.1 "Soaks and owner checkpoints" and rollback row 5; 09 §9.3 boot-time budget and staging; 09 §9.7 (P10); 09 S5 "Done when"; 05-tasks §5T.8.2 row(s) T2, T3; digest `docs/plans/platform-core/digests/S5.md` §6-§8 |

## Preconditions

- `ci/soak.txt` holds a current `S5r1` row (OWNER-13).
- PC-13-01, PC-13-02 have merged; `ci/stage.txt` on `main` reads `shipped_stage: S5` and `release: S5r2` (set by PC-13-01).
- `compat-rehearsal` passed on PC-13-01 (09 §9.2.1 required checks: every PR that changes `release:`).

## Owner steps

1. **Release notes** for the production release that first contains `S5r2`, prepended to the generated body (never
   replacing it): 09 §9.2.1 row 5, "Runs of new task kinds restart after a rollback; one checkpointed job may start over."
   naming the two jobs whose v1 checkpoint moved (`maintenance.recompute-book-aggregates`,
   `maintenance.backfill-file-hashes`), and "Repairs plans made on this release are not visible after a rollback;
   plan again." (the fixer kinds of T3 are new kinds).
2. **Boot-budget A/B** for `S5r2` on the scrubbed production-size staging copy (09 §9.3): `scripts/boot_budget.py
   --prev <bin> --cur <bin> --db-copy <dir> --runs 5`; steady state must meet p50(cur) ≤ max(1.10 × p50(prev),
   p50(prev) + 15 s); append the result to the stage record.
3. **Deploy** the build that carries PC-13-02 to the reference host with the `deploy` recipe; it runs
   `scripts/soak_record.py --deployed` after its ready check (run it by hand after any other deploy). A later deploy
   of a `S5r2` binary keeps the clock; a `make rollback` or another release's binary restarts it.
4. **P10 after each deploy** (09 §9.7): read `<bin>.prev --version-json` and `<bin> --version-json` over ssh, and
   commit the resolved `PREV_REF` as `ci/prev_ref.txt` in a bookkeeping-only PR.
5. **Watch the soak**: `/health` stays `ok` apart from tokens in `ci/soak_health_allow.txt`; no unit exit other than
   0 or 75; no `restore_*` token. Run one Repairs plan and one apply from `/review` after the first deploy and confirm both finish; confirm
   `GET /api/v1/maintenance/jobs` lists the same jobs as before the deploy.
6. **Tick the stage record's done-check list** for this release (`docs/plans/platform-core/stage-ledger/S5.md`):
   - T2: `internal/maintenance` deleted and the build passes; `TestLegacyOpsWriters` at its T2 target (05-tasks §5T.8.2 T2);
   - T3: the Fixer conformance suite (`taskstest.Conformance` over the fixer kinds) green; `TestABOMetricsGatherNoDuplicateOrMissing` green;
   - the per-stage boot budget (step 2) and the R-test cells of `compat-rehearsal` on PC-13-01 (09 §9.3).
7. **Write the row**: once the deploy carrying PC-13-02 has served 14 days, run `scripts/soak_record.py --release
   S5r2` from the operator's checkout over ssh (`DEPLOY_HOST`, `DEPLOY_BIN`, `DEPLOY_STATE_DIR`). It prints the row
   `<release> <landing commit> <binary_id> <commit> <since, UTC> <levels> <evidence SHA-256>` only when every
   criterion passes (zero `restore_*` and boot errors since `<since>`; every owner at level 0 in both
   `--print-storage-levels` and `<db path>.storage-levels`; one health read with no Critical component Down and no
   token outside the allow-list), and refuses with "journal does not cover the soak" when the unit's journal is too
   short (P15). Merge a **bookkeeping-only** PR that adds only that row to `ci/soak.txt`.

## Done when

- `ci/soak.txt` holds a current `S5r2` row written by `scripts/soak_record.py --release S5r2`;
- the stage record ticks the stage's done-check list for `S5r2`;
- P10 ran after each deploy and `ci/prev_ref.txt` is updated.

## Never

- Let an agent run any step of this file, or merge the bookkeeping PR.
- Print session, secret or token values; counts only (09 §9.7).

## Planner notes

- None beyond those of PC-13-01 and PC-13-02 (`TestLegacyOpsWriters` has no creating row; T3's tier).
