<!-- file: docs/agent-tasks/platform-core/OWNER-31-release-h1-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6c642a14-f931-4a62-9386-59018bd55ea8 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-31 — Release H1: fresh verified archive, deploy, soak from the first boot that logs the raised levels, soak row

> **NOT AGENT WORK.** This is an owner checkpoint. No agent may complete it, tick it, run its commands
> against production or the reference host, run `scripts/soak_record.py`, edit `ci/soak.txt`,
> `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, or merge a PR that changes them (09 §9.2.1 "Soaks and owner
> checkpoints"; plan §(g)). An agent that is handed this file stops and reports.

| Field | Value |
|---|---|
| ID | `OWNER-31` |
| Tier | OWNER — NOT AGENT WORK |
| Wave | W21 (H1–H3) |
| Depends on | `PC-21-01`, `PC-21-02`; predecessor row `S10` (OWNER-30) current |
| Release ID | `H1` |
| Duration | 14-day soak from the first boot that logs the raised levels |
| Spec | 09 §9.13 (H1 row); 09 §9.2.1 "Soaks" (H releases); 05-authn §11.2; 05-storage §5S.3.13 |
| Digest | `docs/plans/platform-core/digests/H1-H3.md` §2, §6, §8 |

## Preconditions

- The owner has decided to land H1 on measured data (P1 key counts and row sizes of the identity families; 14 Q-D is deferred to H1), and H1's own stage table exists in 09 §9.13 (written when the owner schedules the cutover, **before its first PR**).
- `ci/soak.txt` holds a current `S10` row (or the previous H release's row, if the owner lands another H first).
- `PC-21-01` and `PC-21-02` (the `H1 flip` PR) have merged.

## Before the deploy: a fresh verified archive, kept off the host (09 §9.13; 05-storage §5S.3.13 item 4)

1. On the release before the cutover, take an archive with reason `pre-cutover` and `core.backup.verify_after_create`
   (`core.backup.create`). Confirm the catalogue shows it with this store's install ID and a passing `VerifyOpen` result
   (`<main>.verify.json`) written after it was created.
2. **Timing:** the archive must be created within `core.storage.cutover.archive_max_age` (default 24 h) **before the
   boot that runs the cutover**, and a level step never runs on the first boot of a new release
   (`RequiresSelfServed`: the binary must first have served for `core.storage.self_served_min`, default 1 h). Plan the
   restart that runs the cutover inside the archive's freshness window.
3. Copy the archive off the host (the backup directory may sit on the store's disk).
4. Clear swap set-asides (a restore's `previous/`/`failed/`, a reset's beside-itself destinations): wait for the kernel's
   sweep after `core.storage.swap.keep_previous`, or set `core.storage.cutover.sweep_set_asides=true` so the cutover boot
   deletes them first. While one exists the step is deferred (`storage_step_pending`, `storage.ErrCutoverResidue`).
5. Check disk headroom (09 §9.7 P12): free space on the DB filesystem ≥ the store size + the checkpoint margin.
6. After the deploy, watch for `storage_step_pending` (archive missing, stale, unverified or foreign; set-aside present):
   the store keeps serving at the old levels and no soak clock starts. Fix the cause and restart.

## Soak and row (09 §9.2.1 "Soaks and owner checkpoints")

1. After **each** deploy of the reference host, record it from the operator's checkout:
   `scripts/soak_record.py --deployed` (appends `{at, binary_id, commit, release}` from the new binary's `--version-json`
   to `.soak/deploys.log`, an untracked scratch directory).
2. Run P10 after each deploy and commit `ci/prev_ref.txt` in a bookkeeping-only PR (09 §9.7 P10).
3. Let the deploy that carries the release's **last** PR serve 14 days (plan §(b) decision 2).
4. Write the row from the operator's checkout over ssh, as `make rollback` does (`DEPLOY_HOST`, `DEPLOY_BIN`,
   `DEPLOY_STATE_DIR`): `scripts/soak_record.py --release H1`. It prints the row
   `<release> <landing commit> <binary_id> <commit> <since, UTC> <levels> <evidence SHA-256>` only when every criterion
   passes, and refuses with "journal does not cover the soak" when the unit's journal is too short (P15).
5. Open a **bookkeeping-only** PR that adds only that row to `ci/soak.txt` (`check_soak.py` check (c) refuses a PR that
   changes `ci/soak.txt` together with any other file) and merge it.

## Pass criteria the script checks

- zero `restore_*` health tokens or rejected journals and zero boot errors since `<since>`; no unit exit other than 0
  or 75;
- **levels:** both the running binary's `--print-storage-levels` and the store's `<db path>.storage-levels` equal the
  predecessor row's `<levels>` with **exactly** the owners this cutover raises (`books`, plus `core` if the H1 stage table moves the identity families to `c/auth/`/`c/rbac/` (09 §9.13)) strictly above it and every
  other owner unchanged (a fixed table in `soak_record.py`, built by PC-21-01); the script writes that vector into the
  row's `<levels>` field. No PR writes levels: `ci/stage.txt` holds no `levels:` line (09 §9.2.1);
- `<since>` is the **later** of the deploy and the first boot that logged the raised levels (the `boot levels=<vector>`
  INFO line), so a deferred boot starts no clock (09 §9.2.1 `<since>`; 09 §9.13).

## Stage-record ticks

Tick in `docs/plans/platform-core/stage-ledger/H1.md` the H1 stage table's done-checks (09 §9.13 minimum: `TestHardeningCutoverRefusedByEveryOlderRelease`, `TestH1H2_DeferredWithoutFreshVerifiedArchive`, `TestH1H2_DeferredWhileSwapSetAsideExists`, `TestH1_NoRawTokenOnDisk`, `TestH1_AmbientScopesPreserved`, `TestH1_ConfinedKeyStaysConfined`, `TestLegacyRoleReachUnchanged`), the archive's ID and its off-host copy, and that the release notes carry H1's text verbatim with the one re-login stated beside it.

## Done when

- `ci/soak.txt` holds a current `H1` row whose levels raise exactly the owners its §9.13 row names.
- Operators were told, with 09 §9.13's H1 note: "Backups made before this release still contain sign-in tokens. They no longer work against this server, but restoring such a backup brings them back until they expire; delete those backups or take new ones."

## One-way release

The cutover raises reader levels, so every older build refuses the store, and `make rollback` and the updater refuse to
swap to one; the floor is this release itself (09 §9.13). Its flip PR can be reverted only while no production release
tag contains it (09 §9.8 revert row; `check_soak.py`). After that, the only way back is a restore of the pre-cutover
archive, which loses everything written since.

## Never

- Let an agent run any command of this file against the reference host, or merge the bookkeeping PR.
- Print secret, session or token values; counts only.

## Planner notes

- No inventory row owns writing H1's stage table and the identity-layout decision (move to `c/auth/`/`c/rbac/` or rewrite in place), which 09 §9.13 requires before H1's first PR; this checkpoint can only verify them.
- The owners H1 raises are undecided until that table exists, so `soak_record.py`'s H1 entry (PC-21-01) waits for it.
