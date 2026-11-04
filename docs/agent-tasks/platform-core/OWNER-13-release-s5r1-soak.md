<!-- file: docs/agent-tasks/platform-core/OWNER-13-release-s5r1-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9de3db99-6524-4aa9-8a4a-29a199ce1148 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-13 — Release S5r1: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> ## ⛔ NOT AGENT WORK
> This is an owner checkpoint. No agent may perform any step below: no agent deploys to the reference host, boots a
> staging copy of production data, runs `scripts/soak_record.py`, edits `ci/soak.txt`, `ci/soak_health_allow.txt` or
> `ci/prev_ref.txt`, ticks the stage record, or merges a PR that changes those files (09 §9.2.1). An agent asked to
> "do OWNER-13" stops and hands this brief to the owner.

**ID:** OWNER-13 · **Wave:** W12 (S5, release `S5r1`, the T0+T1 flip) · **Tier:** OWNER · **Duration:** at least 14 days of soak
**Depends on:** PC-12-08 merged (and with it PC-12-01 … PC-12-07).
**Unblocks:** W13's first release `S5r2` (PC-13-01 merges only on a current `S5r1` row; `check_soak.py` check (b)).
**Owning spec:** 09 S5 (Rollback, Done when); 09 §9.2.1 "Soaks and owner checkpoints", rows 5 and 6; 09 §9.3 boot-time
budget and "Staging"; 09 §9.7 (P1, P10); 05-tasks §5T.6.2 "One writer per family" (the staging smoke); 14 Q-C; digest
`docs/plans/platform-core/digests/S5.md` §6, §8.

## Owner steps

1. **Before the flip merges** (already a gate of PC-12-08): read the P1 census of `opv2:`/`opv3:` undecodable rows
   (09 §9.7 P1) and record the counts in the stage record.
2. **Release notes** for the `S5r1` production release, prepended to the generated body (never replacing it), 09 §9.2.1:
   - row 5: "Runs of new task kinds restart after a rollback; one checkpointed job may start over."
   - row 6: "System-log entries written on this release are not shown after a rollback; they reappear after the upgrade."
3. **Boot-budget A/B** on the scrubbed staging copy (`scripts/boot_budget.py --prev <bin> --cur <bin> --db-copy <dir>
   --runs 5`, `--network none` container; 09 §9.3) and append the result to the stage record.
4. **Staging schedule smoke** (09 S5 Done when; 05-tasks §5T.6.2): boot the `S5r1` binary on the staging copy; confirm
   the boot log prints the scheduler table (name, enabled, interval, next due) and that every enabled definition with an
   interval of 24 h or less fires within one interval; the rest are listed with their next due time. Record pass/fail
   and the table in the stage record.
5. **Deploy** the build carrying `S5r1`'s last PR (PC-12-08 is the last W12 PR) to the reference host; run
   `scripts/soak_record.py --deployed` after each deploy (the `deploy` recipe does it after its ready check).
6. **Check after deploy:** the boot log names books' legacy store as the `tasks` backend; the Activity page shows live
   progress; the journal shows ABO records and the Activity page's system view is unchanged.
7. **P10 after each deploy** (09 §9.7): read `.prev`'s and the current binary's `--version-json`; update
   `ci/prev_ref.txt` in a bookkeeping-only PR.
8. **Watch the soak:** health stays `ok` except tokens on `ci/soak_health_allow.txt`; no unit exit other than 0 or 75;
   every owner at level 0 (`--print-storage-levels` and `<db>.storage-levels`).
9. **Decide Q-C before T7** (14 Q-C): keep the spec default (delete the 15 never-run v2 cron declarations) or name the
   crons to re-add as real schedules; record the decision in the stage record so PC-13-14 can be briefed against it.
   Also decide, at this checkpoint, whether S5 keeps the plan's four later releases (`S5r2` = T2+T3, `S5r3` = T4+T5,
   `S5r4` = T6, `S5r5` = T7) or one release per batch (plan §(b) decision 1).
10. **Tick the stage record's done-check list** for `S5r1` (09 S5 Done when and digest S5 §7 "T0+T1 flip PR"),
    including the `compat-rehearsal` result of PC-12-08 and the resume rehearsal.
11. **Write the row:** once the deploy carrying `S5r1`'s last PR has served 14 days (plan §(b) decision 2), run
    `scripts/soak_record.py --release S5r1` from the operator's checkout and merge the bookkeeping-only PR that adds the
    row to `ci/soak.txt` (that PR changes `ci/soak.txt` alone).

## Done when

- `ci/soak.txt` holds a current `S5r1` row written by `scripts/soak_record.py --release S5r1`;
- the stage record ticks the stage's done-check list (with the staging schedule smoke, the boot-budget A/B and the Q-C
  decision recorded);
- P10 ran after each deploy and `ci/prev_ref.txt` is updated.
