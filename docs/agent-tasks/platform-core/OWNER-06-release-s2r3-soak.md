<!-- file: docs/agent-tasks/platform-core/OWNER-06-release-s2r3-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: c097de21-e614-48d9-8739-b963bf3399b0 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-06 — Release S2r3: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> **NOT AGENT WORK.** This is an owner checkpoint. No agent may complete it, run `scripts/soak_record.py`, edit
> `ci/soak.txt`, `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, deploy to a host, or merge the bookkeeping PR
> (09 §9.2.1 "Soaks and owner checkpoints"; plan §(g) "Each OWNER checkpoint"). An agent may only prepare the
> checklist and draft text for the owner.

| Field | Value |
|---|---|
| ID | OWNER-06 |
| Tier | OWNER — needs the reference host, production reads and 14 days of wall time |
| Wave | W06 (S2, release `S2r3`), after the flip |
| Depends on | PC-06-02 (the S2 r3 flip PR merged) |
| Unblocks | W07 (S2b; its flip PC-07-05 has predecessor `S2r3`) and W08 (UI-0: its first PR needs the `S2r3` row; PC-08-03 also needs step 11 below) |
| Spec | 09 §9.2.1 "Soaks and owner checkpoints" (ledger row, `<since>`, pass criteria); 09 §9.3 "Boot-time budget" and "Rollback rehearsal"; 09 §9.7 P1, P7, P10, P12; 04 "Open items" item 1 (mock conversion cost); 09 §9.4 UI-0 "Preconditions" (reading `reusable-release.yml`, MOD6-5) |
| Digest | `docs/plans/platform-core/digests/S2.md` §2, §9 |

## What the owner does

1. **Before deploying:** confirm the S2 r3 flip PR's `compat-rehearsal` (cell c included) and `unit-lifecycle` runs
   were green, and that the stage record `docs/plans/platform-core/stage-ledger/S2.md` lists the base commit and
   the generated done-check counts the coordinator wrote (plan §(g) step 2).
2. **Deploy** the build that carries the release's last PR to the reference host with the committed recipe, which
   calls `scripts/soak_record.py --deployed` after its ready check (or run it by hand right after the deploy).
3. **P10 after each deploy** (09 §9.7): read `.prev`'s `--print-storage-levels` (or `--print-storage-format`) and
   both binaries' `--version-json`, and commit the resulting `ci/prev_ref.txt` in a bookkeeping-only PR (commit with
   `+dirty` stripped; never `version=dev`).
4. **P7 branch B live check** (09 §9.7 P7, §9.3 cell c): if the ABS surface is in use on the reference host
   (`abs_api_enabled` on, `abs_sess:` sessions present), log in and sync progress with a real ABS client after the
   deploy, and again after any rollback of this release.
5. **Boot-budget A/B** on the scrubbed staging copy: `scripts/boot_budget.py --prev <bin> --cur <bin> --db-copy <dir>
   --runs 5` in a `--network none` container (09 §9.3); append the result to the stage record; steady state must meet
   p50(cur) ≤ max(1.10 × p50(prev), p50(prev) + 15 s).
6. **Credentialed smoke scrape** of `/metrics` on the reference host (05-metrics §6 "Smoke check"): 200 with a
   non-empty family list containing `process_resident_memory_bytes` and `go_goroutines`.
7. **Logging and telemetry check on the host** (new behaviour of this release): the log file is written under the
   state directory at the path the books defaults set (`core.log.output=file`, `core.log.file.path`; release-noted
   path change, 05-logging §11); a `log_level` change made in Settings takes effect without a restart; `/metrics` is
   still served by the legacy gin route and now also lists the kernel's own `audiobook_organizer_kernel_*` families.
8. **Let the deploy carrying the release's last PR serve 14 days** (plan §(b) decision 2: a row is written only once
   that deploy has served 14 days; later deploys inside the release keep the clock, a rollback restarts it).
9. **Tick the stage record's S2 r3 done-check list** (09 S2 "Done when", the r3 items): the contract test switched
   onto the served `Provider.Handler()` with no prefix added to `inScopePrefixes`; its golden diff adds only the
   kernel's own `<namespace>_*` series and renames none; a sampled `:id` legacy route records gin's template as
   `http_route` through otelgin; level change applies; `TestKernelFamiliesGathered` in core;
   `TestHistogramBucketsPinned`; the boot budget.
10. **Write the soak row** from the operator's checkout: `scripts/soak_record.py --release S2r3` (it checks the
    journal covers `<since>`, the binary's commit is on `main` at or after the landing commit, its `ci/stage.txt`
    names `release: S2r3`, every owner is at level 0 in build and store, no `restore_*` or boot errors, and one
    health read with no Critical component down and no token outside `ci/soak_health_allow.txt`). Open and merge the
    bookkeeping-only PR that adds the row to `ci/soak.txt`.
11. **Pre-S2b and pre-UI-0 owner reads and decisions** (plan W06/W07/W08 prerequisites):
    - **P1 key census** (09 §9.7 P1): the read-only db-census command against the reference host; record key counts
      and bytes per family in the stage record (S2b's shadow-claims baseline).
    - **P12 disk headroom** (09 §9.7 P12): `df` of the database's parent versus `du` of the database directory; free
      space must be at least the store size so S2b's pre-migration checkpoint fits; otherwise free space before any
      S2b boot.
    - **Decide the mock conversion** (04 "Open items" item 1): whether the 114 test files on
      `internal/database/mocks` and the ~22 on the hand-written `MockStore` are converted inside the S2b move
      (PC-07-04) or deferred behind the interim `storetest` composite; record the decision in the stage record, where
      PC-07-04's brief reads it.
    - **Read `reusable-release.yml`** at the SHA in `.github/ghcommon-ref.txt` and record what its frontend input
      (`frontend-enabled`) does in the release callers `release-prod.yml` and `prerelease.yml` (09 UI-0
      "Preconditions", MOD6-5); PC-08-03 waits for this record.

## Done when

- `ci/soak.txt` holds a current `S2r3` row written by `scripts/soak_record.py --release S2r3`.
- The stage record ticks the stage's done-check list (step 9).
- Step 11's four records are in the stage record.
- P10 after each deploy, and `ci/prev_ref.txt` updated.

## If something fails

Roll back with `make rollback` (binary swap; r3 writes no state, 09 S2 Rollback; after a rollback the older build
writes its log to HEAD's working-directory path again). The soak clock restarts on the next deploy of an `S2r3`
binary. Record the failure in the stage record and open an issue; no W07 or W08 PR merges until a current `S2r3`
row exists.

## Planner notes

None.
