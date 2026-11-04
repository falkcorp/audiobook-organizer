<!-- file: docs/agent-tasks/platform-core/OWNER-30-release-s10-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: de09383e-53db-463e-9d70-9478028025ca -->
<!-- last-edited: 2026-11-04 -->

# OWNER-30 — Release S10: deploy, 14-day soak, stage-record ticks (every contract deleted), soak row

> **NOT AGENT WORK.** This is an owner checkpoint. No agent may complete it, tick it, run its commands
> against production or the reference host, run `scripts/soak_record.py`, edit `ci/soak.txt`,
> `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, or merge a PR that changes them (09 §9.2.1 "Soaks and owner
> checkpoints"; plan §(g)). An agent that is handed this file stops and reports.

| Field | Value |
|---|---|
| ID | `OWNER-30` |
| Tier | OWNER — NOT AGENT WORK |
| Wave | W20 (S10) |
| Depends on | `PC-20-01`, `PC-20-02`; predecessor row `S9` (OWNER-22) current |
| Release ID | `S10` |
| Duration | 14-day soak |
| Spec | 09 S10; 09 §9.2.1 "Soaks" |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §2, §6, §7 (S10), §8 |

## Preconditions

- `ci/soak.txt` holds a current `S9` row (OWNER-22), and `PC-20-01` (the S10 flip, which sets `shipped_stage:` and `release:` to `S10`) and `PC-20-02` have merged.
- Every contract C3–C9 has passed its W check (OWNER-23 … OWNER-29) and its Gone PR has been in production for one minor before PC-20-02 deleted its alias code (09 §9.5).

## Soak and row (09 §9.2.1 "Soaks and owner checkpoints")

1. After **each** deploy of the reference host, record it from the operator's checkout:
   `scripts/soak_record.py --deployed` (appends `{at, binary_id, commit, release}` from the new binary's `--version-json`
   to `.soak/deploys.log`, an untracked scratch directory).
2. Run P10 after each deploy and commit `ci/prev_ref.txt` in a bookkeeping-only PR (09 §9.7 P10).
3. Let the deploy that carries the release's **last** PR serve 14 days (plan §(b) decision 2).
4. Write the row from the operator's checkout over ssh, as `make rollback` does (`DEPLOY_HOST`, `DEPLOY_BIN`,
   `DEPLOY_STATE_DIR`): `scripts/soak_record.py --release S10`. It prints the row
   `<release> <landing commit> <binary_id> <commit> <since, UTC> <levels> <evidence SHA-256>` only when every criterion
   passes, and refuses with "journal does not cover the soak" when the unit's journal is too short (P15).
5. Open a **bookkeeping-only** PR that adds only that row to `ci/soak.txt` (`check_soak.py` check (c) refuses a PR that
   changes `ci/soak.txt` together with any other file) and merge it.

## Pass criteria the script checks

- zero `restore_*` health tokens or rejected journals and zero boot errors since `<since>`; no unit exit other than 0 or 75;
- every owner at level 0 in both the build (`--print-storage-levels`) and the store's `<db path>.storage-levels` (every release from S0 to S10 ships level 0, 09 §9.2.1).

## Stage-record ticks

Tick in `docs/plans/platform-core/stage-ledger/S10.md` (09 S10 "Done when"):

- 09 §9.6 has no row in state `aliased` except those marked permanent;
- `TestCompatLedgerMatchesCode` shows only permanent entries and `TestNoContractBeforeSunset` is green on the landed commit;
- `internal/serviceregistry`, `pkg/plugin/sdk`, `--db-type` and the SQLite no-op flag are gone from the deployed binary (`<bin> --help` lists neither flag);
- the per-stage boot budget and R-test of 09 §9.3 for S10 (binary swap only; S10 sets no floor, 09 §9.2.2).

## Done when

- `ci/soak.txt` holds a current `S10` row.

## Never

- Let an agent run any command of this file against the reference host, or merge the bookkeeping PR.
- Print secret, session or token values; counts only.

