<!-- file: docs/agent-tasks/platform-core/OWNER-05-release-s2r2-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: bda05424-4a02-4ddf-a2ff-a5e471bc74f4 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-05 — Release S2r2: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> **NOT AGENT WORK.** This is an owner checkpoint. No agent may complete it, run `scripts/soak_record.py`, edit
> `ci/soak.txt`, `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, deploy to a host, or merge the bookkeeping PR
> (09 §9.2.1 "Soaks and owner checkpoints"; plan §(g) "Each OWNER checkpoint"). An agent may only prepare the
> checklist and draft text for the owner.

| Field | Value |
|---|---|
| ID | OWNER-05 |
| Tier | OWNER — needs the reference host, production reads and 14 days of wall time |
| Wave | W05 (S2, release `S2r2`), after the flip |
| Depends on | PC-05-02 (the S2 r2 flip PR merged) |
| Unblocks | every W06 merge (PC-06-01, PC-06-02 carry `Stage-Release: S2 3` / `S2 flip`, whose predecessor is `S2r2`) |
| Spec | 09 §9.2.1 "Soaks and owner checkpoints" (ledger row, `<since>`, pass criteria) and row 12 (where a refused request is counted); 09 §9.3 "Boot-time budget" and "Rollback rehearsal"; 09 §9.7 P7 and P10 |
| Digest | `docs/plans/platform-core/digests/S2.md` §2, §9 |

## What the owner does

1. **Before deploying:** confirm the S2 r2 flip PR's `compat-rehearsal` (cell c included) and `unit-lifecycle` runs
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
7. **Perimeter check on the host** (new behaviour of this release, release-note row 12): a UI page load returns the
   same `Content-Security-Policy` as before (inline-script hash and the Access team host), `X-App-Build` and
   `Reporting-Endpoints` on every response; a cookie-authenticated cross-site POST is refused 403 and logged with
   `security_event`; non-browser clients (no `Sec-Fetch-Site`/`Origin`) are unaffected.
8. **Let the deploy carrying the release's last PR serve 14 days** (plan §(b) decision 2: a row is written only once
   that deploy has served 14 days; later deploys inside the release keep the clock, a rollback restarts it).
9. **Tick the stage record's S2 r2 done-check list** (09 S2 "Done when", the r2 items): `TestCSP_DefaultPerOwner`;
   `X-App-Build` on every response (`TestPerimeter_XAppBuildOnEveryResponse`); the rate-limit class golden;
   `TestCompressionExclusionsEqualHEAD`; the cross-origin test; `TestPerimeterHonoursBlobOnlyValues`; the seeded
   series golden diff empty with the `http_server_*` rows recorded through the real chain and a pre-mount 429 on a
   legacy path recorded with no `http_route`; `TestLegacySeriesLabelValuesMatchHEAD`; `TestHistogramBucketsPinned`;
   the credentialed smoke scrape; the wire goldens; ABS cell c; the bootstrap, invite and ABS limiter tests unchanged;
   the boot budget.
10. **Write the soak row** from the operator's checkout: `scripts/soak_record.py --release S2r2` (it checks the
    journal covers `<since>`, the binary's commit is on `main` at or after the landing commit, its `ci/stage.txt`
    names `release: S2r2`, every owner is at level 0 in build and store, no `restore_*` or boot errors, and one
    health read with no Critical component down and no token outside `ci/soak_health_allow.txt`). Open and merge the
    bookkeeping-only PR that adds the row to `ci/soak.txt`.

## Done when

- `ci/soak.txt` holds a current `S2r2` row written by `scripts/soak_record.py --release S2r2`.
- The stage record ticks the stage's done-check list (step 9).
- P10 after each deploy, and `ci/prev_ref.txt` updated.

## If something fails

Roll back with `make rollback` (binary swap). After a rollback below r2, refused requests are counted by otelgin again
under their route (09 §9.2.1 row 12; the release note says so). The soak clock restarts on the next deploy of an
`S2r2` binary. Record the failure in the stage record and open an issue; no W06 PR merges until a current `S2r2`
row exists.

## Planner notes

None.
