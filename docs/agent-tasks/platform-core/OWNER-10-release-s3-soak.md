<!-- file: docs/agent-tasks/platform-core/OWNER-10-release-s3-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: c68d4554-d1a1-402d-add2-a4dfa468df17 -->
<!-- last-edited: 2026-11-04 -->

# OWNER-10 — Release S3: deploy to the reference host, 14-day soak, stage-record ticks, soak row

> ## ⛔ NOT AGENT WORK
> This is an owner checkpoint. No agent may perform any step below: no agent deploys to the reference host, runs
> `scripts/soak_record.py`, reads production, edits `ci/soak.txt`, `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, or
> merges a PR that changes them (09 §9.2.1). An agent asked to "do OWNER-10" stops and hands this brief to the owner.

**ID:** OWNER-10 · **Wave:** W09 (S3) · **Tier:** OWNER · **Duration:** at least 14 days of soak
**Depends on:** PC-09-01, PC-09-02, PC-09-03, PC-09-04, PC-09-05, PC-09-06, PC-09-07 (all merged).
**Unblocks:** W10 (PC-10-01 needs this row and a current `UI0` row, OWNER-09); contract C3's window starts with
S3's ship date (OWNER-23).
**Owning spec:** 09 §9.2.1 "Soaks and owner checkpoints"; 09 §9.3 (boot-time budget and staging); 09 §9.7 (P4, P10);
digest `docs/plans/platform-core/digests/S3.md` "Owner checkpoints".

## Owner steps

1. **Pre-deploy reads.** Confirm P4 (09 §9.7) still lists only env names that have an alias row or are on 05-config
   §1.12.4's not-alias lists; record counts only in the S3 stage record
   (`docs/plans/platform-core/stage-ledger/S3.md`).
2. **Production-env effective-config golden on the staging copy** (09 S3 Done when; 09 §9.3 "Staging"): provision the
   scrubbed production-size staging copy (`scripts/staging/provision.sh`, `staging-restore`, `staging-scrub`), then run
   `scripts/staging/effective_config_diff.py` (PC-09-02) with the production unit's env names from P4 and secret-class
   values replaced by placeholders. Expected: no differing key; the tool prints key names and value hashes only.
   Commit nothing derived from production; record "equal" (or the differing key names) in the stage record.
3. **Boot-budget A/B** on the staging copy: `scripts/boot_budget.py --prev <bin> --cur <bin> --db-copy <dir> --runs 5`;
   record both values and the absolute ceiling for the first boot that runs a migration step (09 §9.3).
4. **Copy deploy files** changed by PC-09-07 (unit, plist, compose) to the hosts that use them; a deploy-file change
   reaches a host only when copied (09 S2 Deploy).
5. **Deploy** the build carrying the release's last PR (PC-09-07) to the reference host; run
   `scripts/soak_record.py --deployed` after each deploy. The soak counts from the first deploy of a binary at or after
   the landing commit; under the plan's rule (implementation plan §(b) decision 2) the row is written only once the
   deploy carrying the last PR has served 14 days.
6. **P10 after each deploy** (09 §9.7): record `.prev`'s `--version-json` and update `ci/prev_ref.txt` in a
   bookkeeping-only PR.
7. **Watch the soak:** health stays `ok` except tokens listed in `ci/soak_health_allow.txt`; `config_legacy_name_shadowed`
   or `basic_gate_no_password` on the reference host is investigated before the row is written.
8. **Tick the stage record's done-check list** (09 S3 Done when, including the R-test cells run by
   `compat-rehearsal` on the flip PR and the image-job keyring assertions).
9. **Write the row:** from the operator's checkout run `scripts/soak_record.py --release S3`, open a bookkeeping-only
   PR adding the row to `ci/soak.txt`, and merge it.

## Done when

- `ci/soak.txt` holds a current `S3` row written by `scripts/soak_record.py --release S3`;
- the stage record ticks the stage's done-check list;
- P10 ran after each deploy and `ci/prev_ref.txt` is updated.

## Release notes the owner confirms in the S3 production release

09 §9.2.1 rows 1, 2 and 3 (texts in the S3 digest "Rollback costs"), 05-config §1.12.2's "Release note (S3)", the
`ENABLE_AUTH=yes` line (09 §9.2.5), the two 09 §9.2.6 lines (API keys and integration settings; `--log-level` and
`--playlists`), and row 12's Basic-gate 401 metric line. Release notes are prepended to the generated body, never
replacing it.
