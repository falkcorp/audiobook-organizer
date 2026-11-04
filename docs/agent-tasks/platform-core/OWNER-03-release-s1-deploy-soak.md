<!-- file: docs/agent-tasks/platform-core/OWNER-03-release-s1-deploy-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: b9a542b4-dc9e-4c8c-bf3a-48a6a3d60e47 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-03 — Release S1: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> **NOT AGENT WORK.** This is an owner checkpoint. No agent may deploy to the reference host, copy the recipe into
> `Makefile.local`, restore or scrub a production archive, run `scripts/soak_record.py --release`, edit
> `ci/soak.txt`, `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, or merge the bookkeeping PR. An agent may only draft
> what the owner asks for (release-note text, the done-check list) and must say so.

| Field | Value |
|---|---|
| ID | OWNER-03 |
| Tier | OWNER (no agent tier) |
| Wave | W03 (S1, release `S1`) |
| Depends on | PC-03-01, PC-03-02, PC-03-03, PC-03-04, PC-03-05, PC-03-06 |
| Blocks | every W04 merge (`check_soak.py` refuses `release: S2r1` and every `Stage-Release: S2 1` commit until the `S1` row is current and at least 14 days old) |
| Duration | 14 days of soak from the deploy that carries S1's last PR (plan §(b) decision 2: `release: S1` moved in PC-03-01, so later S1 content must still serve 14 days) |
| Digest | `docs/plans/platform-core/digests/S1.md` §2, §6, §7, §8 |
| Spec | 09 §9.2.1 "Soaks and owner checkpoints", row 10; 09 §9.3 "Boot-time budget" and "Staging"; 09 §9.7 P10, P15; 09 S1 rows "Deploy", "Done when" |

## Before the first S1 deploy

1. **P15** (09 §9.7), if OWNER-02 did not already record it: the unit's journal reaches back more than 14 days.
2. **Copy the updated `deploy` recipe** from `Makefile.local.example` into `Makefile.local` again (09 S1 "Deploy": the
   recipe now refuses while a restore is staged or in flight, override `ROLLBACK_IGNORE_RESTORE=1` for a pending
   intent only).
3. **Release note**: prepend 09 §9.2.1 row 10's text to the generated notes of the release that carries S1 (CLAUDE.md:
   prepend, never replace): "A restore now takes effect at the next restart, which the service performs itself. A
   container started without a restart policy must be started again. Rolling back while a restore is staged cancels
   it; after such a rollback, updates from Settings are refused until the upgraded release has started once, and the
   upgrade itself needs the same override."
4. **Fresh archive** taken after the reference host ran S0 (09 §9.3 step 1: a pre-S0 archive cannot be staged), with
   its `<archive>.manifest.json`.

## Deploy, staging data point, soak

5. Deploy S1 with `make deploy`; run P10 and update `ci/prev_ref.txt` after **each** reference-host deploy.
6. Smoke checks on the host: `<bin> --print-storage-levels` prints every owner at 0; the credentialed `/metrics`
   scrape answers 200 with a non-empty family list containing `process_resident_memory_bytes` and `go_goroutines`
   (09 S1 "Done when"; 05-metrics §6 "Smoke check").
7. **Staging copy and the boot-budget A/B, first data point** (09 §9.3; 09 S1 "Done when"): on a linux/amd64 machine,
   `scripts/staging/provision.sh` with the fresh archive (it fetches the writer binary by `binary_id`, runs
   `staging-restore`, `staging-scrub --confirm-destroy-credentials`, and boots with `--network none`); then
   `scripts/boot_budget.py --prev <S0 bin> --cur <S1 bin> --db-copy <dir> --runs 5 --append docs/plans/platform-core/stage-ledger/S1.md`.
   Record the steady-state verdict (p50(cur) ≤ max(1.10 × p50(prev), p50(prev) + 15 s)) and the first-migration-boot
   ceiling you set for later stages.
8. **Restore rehearsal on the staging copy** (09 S1 "Done when"): stage a restore of the archive through the copy's
   API, let the service exit 75 and come back, and confirm the store equals the archive.
9. Let the deploy that carries S1's last PR serve 14 days. Any `make rollback` restarts the clock.

## Close the checkpoint

10. Tick every item of 09 S1 "Done when" in the stage record, with the boot-budget row and the R-test cell e result
    from `compat-rehearsal`.
11. From the operator's checkout: `scripts/soak_record.py --release S1`.
12. Merge the bookkeeping-only PR that adds the `S1` row to `ci/soak.txt`.

## Done when

- `ci/soak.txt` holds a current `S1` row written by `scripts/soak_record.py --release S1`.
- The stage record ticks every S1 done-check, including the first boot-budget data point.
- P10 was recorded after each deploy and `ci/prev_ref.txt` is current.

## Rollback

`make rollback`; 09 §9.2.1 row 10 applies (refused while a restore is staged or in flight; with
`ROLLBACK_IGNORE_RESTORE=1` a staged intent is discarded by the roll-forward boot). A rollback restarts the soak clock.
