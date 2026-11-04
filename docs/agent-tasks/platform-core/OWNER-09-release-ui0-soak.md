<!-- file: docs/agent-tasks/platform-core/OWNER-09-release-ui0-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0a9de9b3-90d1-4b71-a285-7967a488b2ea -->
<!-- last-edited: 2026-11-04 -->

# OWNER-09 — Release UI0: deploy, 14-day soak, stage-record ticks, soak row

> **NOT AGENT WORK.** Owner checkpoint (09 §9.2.1). No agent may run `scripts/soak_record.py`, deploy to the
> reference host, edit `ci/soak.txt`, `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, tick the stage record, or
> merge a PR that changes those files. An agent handed this file stops and reports.

| Field | Value |
|---|---|
| ID | OWNER-09 |
| Tier | OWNER (no agent tier) |
| Wave | W08 (UI-0, release `UI0`) |
| Depends on | PC-08-01, PC-08-02, PC-08-03 (all merged) |
| Unblocks | the `S4r1` marker change (PC-10-01 needs a current `UI0` row as well as the `S3` row) |
| Duration | 14 days from `<since>`; UI0 soaks in parallel, off the serial chain, beside S2br1's and later soaks |
| Stage record | `docs/plans/platform-core/stage-ledger/UI-0.md` |

The reading of `reusable-release.yml` that PC-08-03 needs is OWNER-06's, not this checkpoint's.

## Steps

1. Deploy a `main` build carrying all of UI-0 (at or after PC-08-03's merge commit) to the reference host;
   `scripts/soak_record.py --deployed` runs after the deploy. For `UI0`, a later `main` binary of another release
   before `S4r1` also counts toward `<since>`, so S2b's and S3's deploys do not restart UI0's clock (09 §9.2.1).
2. Confirm the first RC that `prerelease.yml` cut after PC-08-03 is green and its build left the checkout clean
   (the CI half of UI-0's "dry-run RC" done-check; PC-08-03 verified the same commands locally).
3. P10 after each deploy (as OWNER-07 step 6); commit `ci/prev_ref.txt` alone in a bookkeeping PR.
4. Boot-budget A/B on the scrubbed staging copy (`scripts/staging/provision.sh`, production archive):

   ```bash
   python3 scripts/boot_budget.py --prev <bin-before-UI0> --cur <bin-UI0> --db-copy <staging-dir> --runs 5
   ```
   Pass: p50(cur) ≤ max(1.10 × p50(prev), p50(prev) + 15 s). Append the result to the stage record.
5. After 14 days, from the operator's checkout:

   ```bash
   python3 scripts/soak_record.py --release UI0
   ```
   Pass criteria: zero `restore_*` and boot errors since `<since>`; every owner at level 0 in the build and the
   store; a clean health read against `ci/soak_health_allow.txt`; the journal covers the soak. The binary check
   accepts `release: UI0` or a later release before `S4r1`.
6. Tick the UI-0 done-check list in the stage record (digest "Done-when"), then open a bookkeeping-only PR that
   changes `ci/soak.txt` alone with the printed row.

## Done when

- `ci/soak.txt` holds a current `UI0` row written by `scripts/soak_record.py --release UI0`.
- The stage record ticks the stage's done-check list.
- P10 ran after each deploy and `ci/prev_ref.txt` is updated.
