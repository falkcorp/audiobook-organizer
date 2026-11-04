<!-- file: docs/agent-tasks/platform-core/OWNER-02-release-s0-deploy-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7579c6ed-c5b4-418b-a397-227c5d18f78e -->
<!-- last-edited: 2026-11-04 -->

# OWNER-02 — Release S0: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> **NOT AGENT WORK.** This is an owner checkpoint. No agent may run any step below, run
> `scripts/soak_record.py --release`, edit `ci/soak.txt`, `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, deploy to
> the reference host, change its sudoers or `Makefile.local`, or merge the bookkeeping PR. An agent may only prepare
> drafts the owner asks for (release-note text, the list of done-checks to tick) and must say so in its report.

| Field | Value |
|---|---|
| ID | OWNER-02 |
| Tier | OWNER (no agent tier) |
| Wave | W02 (S0, release `S0`) |
| Depends on | PC-00-01, PC-00-02, PC-00-03, PC-00-04, PC-01-01, PC-01-02, PC-01-03, PC-01-04, PC-01-05, PC-01-06, PC-01-07, PC-02-01, PC-02-02, PC-02-03, PC-02-04, PC-02-05, PC-02-06, PC-02-07 |
| Blocks | every W03 merge (`check_soak.py` refuses `release: S1` and every `Stage-Release: S1 1` commit until the `S0` row is current and at least 14 days old) |
| Duration | 14 days of soak from the deploy that carries S0's last PR (plan §(b) decision 2) |
| Digest | `docs/plans/platform-core/digests/S0.md` §9; `digests/S1.md` §2 |
| Spec | 09 §9.2.1 "Soaks and owner checkpoints" and "The soak ledger"; 09 §9.2.2 "Owner actions, all in S0"; 09 §9.3 "Boot-time budget" and "Staging"; 09 §9.7 P10, P15; 09 S0 rows "Deploy", "Docker" (stage-record facts) |

## Before the first S0 deploy (owner actions of 09 §9.2.2 and 09 S0)

1. **Sudoers lines** on the reference host, beside the existing drop-in (`docs/system/deploy-and-gpu-ops.md`, sudoers
   section, as PC-02-04 updated it): `<DEPLOY_DB>.storage-levels`, `<DEPLOY_DB>.storage-applied`, the one restore-probe
   `find` over `restore/` (matching `restore/pending.json`, `restore/*/inflight.json`,
   `restore/*/inflight-checkpoint.json`), the read-only `journalctl -u <unit>` line, and the binary-history
   `mkdir`/`cp`/`rm` lines. Without the first, every rollback refuses.
   Check: `bash scripts/deploy-preflight.sh origin/main` with `DEPLOY_HOST`, `DEPLOY_DB` and `DEPLOY_STATE_DIR` set
   passes the sudoers check.
2. **Copy the updated `deploy` recipe** from `Makefile.local.example` into the uncommitted `Makefile.local`
   (timeout variable, ssh arguments, `DEPLOY_STATE_DIR`, store check, binary history, `soak_record.py --deployed`).
3. **Bookkeeping-only PR** (plan §(b) decision 5), containing nothing else, with `Stage-Release: none`:
   `ci/soak.txt` (empty), `ci/soak_health_allow.txt` seeded with 05-health §5H.3.1's tokens (`auto_update_paused`,
   `legacy_config_keys`, `config_legacy_name_shadowed`, `auth_disabled_remote`, `revocation_journal_colocated`, the
   `core.integrations.<capability>.<instance>` reasons), and `ci/prev_ref.txt` (from step 5's P10 output).
4. **Stage-record facts** (09 S0 "Docker"): read `reusable-release.yml` and record which Dockerfile it builds and
   whether the release publishes the raw updater asset (`audiobook-organizer-<os>-<arch>`); record the first CI p50
   reading (09 §9.2.10) in `docs/plans/platform-core/stage-ledger/S0.md`.
5. **P10** (09 §9.7): `ssh <host> '<bin>.prev --print-storage-levels || <bin>.prev --print-storage-format'`,
   `ssh <host> '<bin>.prev --version-json'` and `ssh <host> '<bin> --version-json'`; derive `PREV_REF` by 09 §9.7's
   rule (commit without `+dirty`; else the describe `g<hex>`; never `version=dev`).
6. **P15** before S1 (09 §9.7): the unit's journal reaches back more than 14 days, or raise journald retention first.

## Deploy and soak

7. Deploy S0 with `make deploy` (the copied recipe). It runs the store check, waits with `DEPLOY_READY_TIMEOUT`,
   keeps the binary under `<bin>.d/<binary_id>` and records the deploy with `scripts/soak_record.py --deployed`.
   Re-run P10 and update `ci/prev_ref.txt` after **each** reference-host deploy.
8. Smoke checks on the host: `<bin> --print-storage-levels` prints every owner at 0 (`books 0`, `core 0`);
   `curl -s -o /dev/null -w '%{http_code}' <abo-url>/api/core/v1/ping` prints `404`; `/health` reports `version`;
   a response carries `X-App-Build`; the credentialed scrape answers 200 with `process_resident_memory_bytes` and
   `go_goroutines`; health shows `auto_update_paused` while auto-update is enabled.
9. Let the deploy that carries S0's last PR serve 14 days (plan §(b) decision 2). Any `make rollback` restarts the clock.

## Close the checkpoint

10. Tick every item of 09 S0 "Done when" in the stage record (S0 has no boot-budget data point: a pre-S0 archive
    cannot be staged, 09 §9.3).
11. From the operator's checkout: `scripts/soak_record.py --release S0` (over ssh). It refuses when the journal does
    not cover the soak, a logged boot ran another binary, any `restore_*` token or boot error appears, or any owner
    is not at 0.
12. Merge the bookkeeping-only PR that adds the `S0` row to `ci/soak.txt`.

## Done when

- `ci/soak.txt` holds a current `S0` row written by `scripts/soak_record.py --release S0`.
- The stage record ticks every S0 done-check.
- P10 was recorded after each deploy and `ci/prev_ref.txt` is current.
- A fresh archive was taken after the reference host ran S0 (needed for staging from S1 on; 09 §9.3 step 1).

## Rollback

`make rollback` (binary swap). S0's state is ignored by older builds (09 S0 "Rollback"); a rollback restarts the
soak clock.
