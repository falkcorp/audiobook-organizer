<!-- file: docs/agent-tasks/platform-core/OWNER-07-release-s2br1-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: bf7b6142-6e3d-4359-92d1-d2f408643655 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-07 — Release S2br1: deploy, 14-day soak, stage-record ticks, soak row

> **NOT AGENT WORK.** This is an owner checkpoint (09 §9.2.1 "Soaks and owner checkpoints"). No agent may run
> `scripts/soak_record.py`, deploy to the reference host, edit `ci/soak.txt`, `ci/soak_health_allow.txt` or
> `ci/prev_ref.txt`, tick the stage record, or merge a PR that changes those files. An agent handed this file
> stops and reports that it is owner work.

| Field | Value |
|---|---|
| ID | OWNER-07 |
| Tier | OWNER (no agent tier) |
| Wave | W07 (stage S2b, release `S2br1`, shadow claims) |
| Depends on | PC-07-05, PC-07-06, PC-07-07, PC-07-08 (all merged to `main`) |
| Unblocks | PC-07-09 (the `release: S2br2` change and every `Stage-Release: S2b 2` PR) |
| Duration | 14 days from `<since>`, counted only once the deploy carrying the release's last PR (PC-07-08) has served (plan decision 2) |
| Stage record | `docs/plans/platform-core/stage-ledger/S2b.md` |

## Before the first S2b deploy

1. **P12 disk headroom** (09 §9.7). The first S2b boot runs books' new versioned steps (the three settings
   one-shots and the key migrations), so it takes a pre-migration checkpoint:

   ```bash
   ssh <host-a> 'df -B1 --output=avail <db-dir-parent>; du -sb <db-dir>'
   ```
   Pass: available ≥ store size. Otherwise free space first; the boot fails closed (`TestCheckpointFreeSpaceRefused`).
2. **P1 census counts** (S2b row of 09 §9.7), read-only:

   ```bash
   curl -sS -H "Authorization: Bearer <redacted-token>" "<abo-url>/api/v1/diagnostics/db-census?fresh=true" \
   | jq '.data.families[] | select(.prefix | test("^(u|role|apikey|invite|inviteallow|oauthid|sess|idx|setting|opv2|opv3|act):")) | {prefix,keys,raw_key_bytes,raw_value_bytes}'
   ```
   Record the output in the stage record (never values; the census prints counts only).
3. **Boot-budget A/B and the first-migration ceiling** (09 §9.3) on a scrubbed staging copy built by
   `scripts/staging/provision.sh` from a fresh production archive:

   ```bash
   python3 scripts/boot_budget.py --prev <bin-S2r3> --cur <bin-S2br1> --db-copy <staging-dir> --runs 5
   ```
   Pass (steady state): p50(cur) ≤ max(1.10 × p50(prev), p50(prev) + 15 s). Record the result and an absolute
   ceiling for the first boot that runs a migration step (for example ≤ 2 × steady state + the checkpoint),
   reported per `<namespace>_kernel_boot_phase_seconds{phase,step}`.
4. **Install the checkpoint alert snippet** that ships with S2b (on `<namespace>_storage_checkpoint_bytes`).

## Deploy and inventory (each deploy of the release)

5. Deploy a `main` build at or after PC-07-08's merge commit with the committed `deploy` recipe; it runs
   `scripts/soak_record.py --deployed` after its ready check (or run that by hand after a manual deploy).
6. **P10 rollback inventory** after each deploy:

   ```bash
   ssh <host-a> '<bin>.prev --print-storage-levels || <bin>.prev --print-storage-format'
   ssh <host-a> '<bin>.prev --version-json'; ssh <host-a> '<bin> --version-json'
   ```
   Commit the derived `PREV_REF` to `ci/prev_ref.txt` in a bookkeeping-only PR (that file alone): `commit` with a
   trailing `+dirty` stripped; else the `g<hex>` of a describe-form `version`; else a manual ref; never
   `version=dev`.

## After 14 days: the soak row

7. From the operator's checkout, with `DEPLOY_HOST`, `DEPLOY_BIN`, `DEPLOY_STATE_DIR` set as for `make rollback`:

   ```bash
   python3 scripts/soak_record.py --release S2br1
   ```
   It prints the row only when every criterion passes:
   - zero `restore_*` and boot errors since `<since>`; no unit exit other than 0 or 75;
   - every owner at level 0 in both the build (`--print-storage-levels`) and the store (`<db>.storage-levels`);
   - one health read at record time with no Critical component Down and no token outside
     `ci/soak_health_allow.txt`;
   - **the persisted claim-violation record (`c/storage/claimviol/`, shadow mode) holds no entry whose `last_at`
     is at or after `<since>`** (an older entry for a writer fixed earlier is allowed);
   - the journal covers the soak (P15).
8. Tick the S2b done-check list in the stage record (09 S2b "Done when"; digest section 8), then open a
   bookkeeping-only PR that changes `ci/soak.txt` alone with the printed row, and merge it yourself.

## Done when

- `ci/soak.txt` holds a current `S2br1` row written by `scripts/soak_record.py --release S2br1`.
- The stage record ticks the stage's done-check list.
- P10 ran after each deploy and `ci/prev_ref.txt` is updated.

## Planner notes

- **How the claim-violation record is read at S2br1 is not specified.** 05-storage §5S.9.4 names the census
  (`c/storage/census`) and `GET /api/core/v1/storage/claim-violations` (`core:system.manage`), but core routes and
  the permission arrive at S4; `soak_record.py` (S0) is not specified to read `c/storage/claimviol/`. Before this
  checkpoint, someone must decide the read path (an offline read through `app.OpenOffline` read-only in a
  subcommand, or a census field), or the S2br1 criterion cannot be evaluated.
