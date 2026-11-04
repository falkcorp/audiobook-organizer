<!-- file: docs/agent-tasks/platform-core/OWNER-21-release-s8-soak-row.md -->
<!-- version: 1.0.0 -->
<!-- guid: f812443d-ea95-44cf-b3ae-81e0a7fe4481 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-21 — Release S8: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> **NOT AGENT WORK.** No agent may complete, simulate or shortcut any step of this checkpoint: no agent runs
> `scripts/soak_record.py`, deploys to the reference host, reads production, edits `ci/soak.txt`,
> `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, or merges a PR that changes those files (09 §9.2.1 "Soaks and
> owner checkpoints"). An agent may only remind the owner that this checkpoint blocks W18.

| Field | Value |
|---|---|
| ID | OWNER-21 |
| Tier | OWNER — no agent may complete it |
| Wave | W17 (closes release `S8`) |
| Depends on | PC-17-04 (the S8 flip) and PC-17-05 (the UI calls, the release's last PR) merged and deployed |
| Blocks | every W18 merge (PC-18-01 … PC-18-04: `Stage-Release: S9 …` needs a current `S8` row) |
| Duration | 14 days of soak from the deploy that carries PC-17-05 (plan §(b) decision 2: content after the flip) |
| Spec | 09 §9.2.1 "Soaks and owner checkpoints"; 09 §9.3 boot-time budget and "Staging"; 09 §9.7 (P10); 09 S8 "Done when" |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §7 S8, §8 |

## Owner steps

1. **Deploy** the `main` build that contains PC-17-05 to the reference host with the committed `deploy` recipe
   (which runs `scripts/soak_record.py --deployed` after its ready check; run it by hand after any manual deploy).
   The recipe and `make rollback` must refuse while a restore is pending or in flight; confirm none is.
2. **P10 after each deploy** (09 §9.7): read `<bin>.prev --version-json` and `<bin> --version-json` and commit the
   resolved `PREV_REF` as `ci/prev_ref.txt` in a bookkeeping-only PR.
3. **Staging checks** on the scrubbed production-size copy (09 §9.3): the boot-budget A/B with
   `scripts/boot_budget.py --prev <S7 bin> --cur <S8 bin> --db-copy <dir> --runs 5`; record p50s and any
   `<namespace>_kernel_boot_phase_seconds` reading in the stage record.
4. **Credentialed smoke scrape** of `/metrics` on the reference host (any non-confined credential): 200 with a
   non-empty family list containing `process_resident_memory_bytes`, `go_goroutines` and `<namespace>_build_info`
   (05-metrics §6). An unauthenticated scrape gets 401.
5. **Backup on the host** (read-only observation): the first `core.backup.create` writes a plaintext
   `mediacore-backup/1` main archive under the legacy name pattern; existing `audiobooks_*` archives are listed as
   `legacy-abo/0`. Do **not** restore on the reference host as part of this checkpoint.
6. **Tick the stage record** (`docs/plans/platform-core/stage-ledger/S8.md`) against 09 S8 "Done when", item by item:
   the seeded series golden (books names equal S7's, `http_server_*` otelgin's, diff adds only `<namespace>_build_info`
   and new core families), `TestHistogramBucketsPinned`, the smoke scrape, `promtool test rules
   deploy/prometheus/alert-rules.test.yml`, `internal/startup` deleted, the restore matrix (S0 archive on S8; S8
   archive on S7 and HEAD; below-floor refused; staged intent across a rollback), R-test cell e,
   `TestApply_RawAssetNoBackup`, S0's updater test unchanged, `TestABOUpdaterSourceMatchesHEAD`, cells a/b for the
   moved UI calls, `/livez/` and `/READYZ` 404. Record the generated alias counts beside 09's informative ones.
7. **Release notes** of the S8 production release carry row 11's line (09 §9.2.1), prepended to the generated notes:
   "If you restore a backup on this release, restore one while rolled back, or restore one made before a rollback,
   everyone signs in again and every API key must be re-enabled on the keys page by its owner or an admin."
8. **Soak row.** After 14 days with every pass criterion met (zero `restore_*` and boot errors since `<since>`, every
   owner at level 0 in both `--print-storage-levels` and `<db>.storage-levels`, one health read with no Critical
   component Down and no token outside `ci/soak_health_allow.txt`), run from the operator's checkout:

   ```bash
   scripts/soak_record.py --release S8
   ```

   Add the printed row to `ci/soak.txt` in a bookkeeping-only PR and merge it.

## Done when

- `ci/soak.txt` holds a current `S8` row written by `scripts/soak_record.py --release S8`.
- The stage record ticks the stage's done-check list (step 6).
- P10 ran after each deploy and `ci/prev_ref.txt` was updated.

## Planner notes

None.
