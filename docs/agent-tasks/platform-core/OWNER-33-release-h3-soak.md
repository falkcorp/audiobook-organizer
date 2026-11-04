<!-- file: docs/agent-tasks/platform-core/OWNER-33-release-h3-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5ad52ff3-0f8b-43e5-b278-2963e74c8886 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-33 — Release H3: deploy, soak from the first boot that logs the raised levels, soak row

> **NOT AGENT WORK.** This is an owner checkpoint. No agent may complete it, tick it, run its commands
> against production or the reference host, run `scripts/soak_record.py`, edit `ci/soak.txt`,
> `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, or merge a PR that changes them (09 §9.2.1 "Soaks and owner
> checkpoints"; plan §(g)). An agent that is handed this file stops and reports.

| Field | Value |
|---|---|
| ID | `OWNER-33` |
| Tier | OWNER — NOT AGENT WORK |
| Wave | W21 (H1–H3) |
| Depends on | `PC-21-04`; predecessor row: the H release `release:` names when H3's first PR merges |
| Release ID | `H3 (or H2H3)` |
| Duration | 14-day soak from the first boot that logs the raised levels |
| Spec | 09 §9.13 (H3 row); 09 §9.2.1 "Soaks" (H releases); 05-config §1.12.5 |
| Digest | `docs/plans/platform-core/digests/H1-H3.md` §2, §6, §8 |

## Preconditions

- H3's own stage table exists in 09 §9.13, and the settings family's cutover has shipped or ships in the same release (09 §9.13; 05-config §1.12.5: H3 ships with or after it).
- The predecessor row is current; `PC-21-04` (the `H3 flip` or `H2H3 flip` PR) has merged.
- 09 §9.13 asks for a fresh verified archive before H1 and H2 only; H3 has no such precondition, but taking one before any one-way release is still the owner's choice.

## Soak and row (09 §9.2.1 "Soaks and owner checkpoints")

1. After **each** deploy of the reference host, record it from the operator's checkout:
   `scripts/soak_record.py --deployed` (appends `{at, binary_id, commit, release}` from the new binary's `--version-json`
   to `.soak/deploys.log`, an untracked scratch directory).
2. Run P10 after each deploy and commit `ci/prev_ref.txt` in a bookkeeping-only PR (09 §9.7 P10).
3. Let the deploy that carries the release's **last** PR serve 14 days (plan §(b) decision 2).
4. Write the row from the operator's checkout over ssh, as `make rollback` does (`DEPLOY_HOST`, `DEPLOY_BIN`,
   `DEPLOY_STATE_DIR`): `scripts/soak_record.py --release H3 (or H2H3)`. It prints the row
   `<release> <landing commit> <binary_id> <commit> <since, UTC> <levels> <evidence SHA-256>` only when every criterion
   passes, and refuses with "journal does not cover the soak" when the unit's journal is too short (P15).
5. Open a **bookkeeping-only** PR that adds only that row to `ci/soak.txt` (`check_soak.py` check (c) refuses a PR that
   changes `ci/soak.txt` together with any other file) and merge it.

## Pass criteria the script checks

- zero `restore_*` health tokens or rejected journals and zero boot errors since `<since>`; no unit exit other than 0
  or 75;
- **levels:** both the running binary's `--print-storage-levels` and the store's `<db path>.storage-levels` equal the
  predecessor row's `<levels>` with **exactly** the owners this cutover raises (`books` only (05-config §1.12.5)) strictly above it and every
  other owner unchanged (a fixed table in `soak_record.py`, built by PC-21-01); the script writes that vector into the
  row's `<levels>` field. No PR writes levels: `ci/stage.txt` holds no `levels:` line (09 §9.2.1);
- `<since>` is the **later** of the deploy and the first boot that logged the raised levels (the `boot levels=<vector>`
  INFO line), so a deferred boot starts no clock (09 §9.2.1 `<since>`; 09 §9.13).

## Stage-record ticks

Tick in `docs/plans/platform-core/stage-ledger/H3.md` the H3 stage table's done-checks (09 §9.13 minimum: auth enabled through the environment stays on across restarts; `TestHardeningCutoverRefusedByEveryOlderRelease`; `TestComposeEnvKeepsUIBasicGate`; 05-config §1.12.5 adds `TestLegacyAuthEnablePrecedenceMatchesHEAD`'s disabling half) and H3's release-note text verbatim.

## Done when

- `ci/soak.txt` holds a current `H3` row whose levels raise exactly the owners its §9.13 row names.
- Operators were told, with 09 §9.13's H3 note: "With authentication off, Settings can no longer turn it on; set `ENABLE_AUTH=true` or `ABO_CORE_AUTH_MODE=required` and restart."

## One-way release

The cutover raises reader levels, so every older build refuses the store, and `make rollback` and the updater refuse to
swap to one; the floor is this release itself (09 §9.13). Its flip PR can be reverted only while no production release
tag contains it (09 §9.8 revert row; `check_soak.py`). After that, the only way back is a restore of the pre-cutover
archive, which loses everything written since.

## Never

- Let an agent run any command of this file against the reference host, or merge the bookkeeping PR.
- Print secret, session or token values; counts only.

## Planner notes

- No inventory row owns writing H3's stage table, and the settings-family cutover H3 depends on is deferred-build with no design (spec README "Build scope").
