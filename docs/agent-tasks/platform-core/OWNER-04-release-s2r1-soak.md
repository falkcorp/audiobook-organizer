<!-- file: docs/agent-tasks/platform-core/OWNER-04-release-s2r1-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: 49fef51c-5eb0-4b5e-b3b3-38dc40770765 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-04 — Release S2r1: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> **NOT AGENT WORK.** This is an owner checkpoint. No agent may complete it, run `scripts/soak_record.py`, edit
> `ci/soak.txt`, `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, deploy to a host, or merge the bookkeeping PR
> (09 §9.2.1 "Soaks and owner checkpoints"; plan §(g) "Each OWNER checkpoint"). An agent may only prepare the
> checklist and draft text for the owner.

| Field | Value |
|---|---|
| ID | OWNER-04 |
| Tier | OWNER — needs the reference host, production reads and 14 days of wall time |
| Wave | W04 (S2, release `S2r1`), after the flip |
| Depends on | PC-04-07 (the S2 r1 flip PR merged) |
| Unblocks | every W05 merge (PC-05-01, PC-05-02 carry `Stage-Release: S2 2` / `S2 flip`, whose predecessor is `S2r1`) |
| Spec | 09 §9.2.1 "Soaks and owner checkpoints" (ledger row, `<since>`, pass criteria); 09 §9.3 "Boot-time budget" and "Rollback rehearsal"; 09 §9.7 P7 and P10; 09 §9.4 S2 "Deploy" (owner action: install the unit/plist and daemon-reload) |
| Digest | `docs/plans/platform-core/digests/S2.md` §2, §9 |

## What the owner does

1. **Before deploying:** confirm the S2 r1 flip PR's `compat-rehearsal` (cell c included) and `unit-lifecycle` runs
   were green, and that the stage record `docs/plans/platform-core/stage-ledger/S2.md` lists the base commit and
   the generated done-check counts the coordinator wrote (plan §(g) step 2).
2. **Install the updated supervisor files on each host** (09 S2 Deploy: "a unit change reaches a host only when
   copied"): copy `deploy/audiobook-organizer.service` into place and run `systemctl daemon-reload`; on a launchd
   host, install `deploy/com.audiobook-organizer.plist` the way the deploy recipe documents. Check the installed
   unit shows `TimeoutStopSec=60`, `KillMode=mixed`, `RestartPreventExitStatus=78`.
3. **Deploy** the build that carries the release's last PR to the reference host with the committed recipe, which
   calls `scripts/soak_record.py --deployed` after its ready check (or run it by hand right after the deploy).
4. **P10 after each deploy** (09 §9.7): read `.prev`'s `--print-storage-levels` (or `--print-storage-format`) and
   both binaries' `--version-json`, and commit the resulting `ci/prev_ref.txt` in a bookkeeping-only PR (commit with
   `+dirty` stripped; never `version=dev`).
5. **P7 branch B live check** (09 §9.7 P7, §9.3 cell c): if the ABS surface is in use on the reference host
   (`abs_api_enabled` on, `abs_sess:` sessions present), log in and sync progress with a real ABS client after the
   deploy, and again after any rollback of this release.
6. **Boot-budget A/B** on the scrubbed staging copy: `scripts/boot_budget.py --prev <bin> --cur <bin> --db-copy <dir>
   --runs 5` in a `--network none` container (09 §9.3); append the result to the stage record; steady state must meet
   p50(cur) ≤ max(1.10 × p50(prev), p50(prev) + 15 s).
7. **Credentialed smoke scrape** of `/metrics` on the reference host (05-metrics §6 "Smoke check"): 200 with a
   non-empty family list containing `process_resident_memory_bytes` and `go_goroutines`.
8. **Shutdown check on the host:** one `systemctl stop` while an operation runs; the journal shows the run stamped
   interrupted and no SIGKILL.
9. **Let the deploy carrying the release's last PR serve 14 days** (plan §(b) decision 2: a row is written only once
   that deploy has served 14 days; later deploys inside the release keep the clock, a rollback restarts it).
10. **Tick the stage record's S2 r1 done-check list** (09 S2 "Done when", the r1 items): `main.go` has no
    `server.NewServer`; the serviceregistry grep; `coretest` boots with zero modules; `go build ./...`;
    `TestLegacyManifest_RegistersAsMuxPatterns` (ABS on and off), `TestCollisionRule_RefusesUnmergeableSets`,
    `TestLegacyMount_GeneralisedPatternAcceptsAnyMember`, `TestS2Manifest_SPermRoutesCarryLegacyAtom`,
    `TestLegacyMount_MuxAndGinAgreeOnEncodedSlash`; `TestStateDirFallbackMatchesHEAD` and
    `TestStateDir_OneResolverYAMLDBPath` (unit and image); the 20 wire goldens; ABS cell c; the guard assertion;
    invite and bootstrap limiter tests unchanged; unit-lifecycle's stop check; `TestHistogramBucketsPinned`; the
    credentialed smoke scrape and the seeded series test with no removed family; the boot budget.
11. **Write the soak row** from the operator's checkout: `scripts/soak_record.py --release S2r1` (it checks the
    journal covers `<since>`, the binary's commit is on `main` at or after the landing commit, its `ci/stage.txt`
    names `release: S2r1`, every owner is at level 0 in build and store, no `restore_*` or boot errors, and one
    health read with no Critical component down and no token outside `ci/soak_health_allow.txt`). Open and merge the
    bookkeeping-only PR that adds the row to `ci/soak.txt`.

## Done when

- `ci/soak.txt` holds a current `S2r1` row written by `scripts/soak_record.py --release S2r1`.
- The stage record ticks the stage's done-check list (step 10).
- P10 after each deploy, and `ci/prev_ref.txt` updated.

## If something fails

Roll back with `make rollback` (binary swap; S2 r1 writes no state, 09 S2 Rollback). The soak clock restarts on the
next deploy of an `S2r1` binary. Record the failure in the stage record and open an issue; no W05 PR merges until a
current `S2r1` row exists.

## Planner notes

None.
