<!-- file: docs/agent-tasks/platform-core/OWNER-08-release-s2br2-soak.md -->
<!-- version: 1.0.0 -->
<!-- guid: dc453110-3568-450c-8f7b-2910165f549c -->
<!-- last-edited: 2026-11-04 -->

# OWNER-08 — Release S2br2: deploy, 14-day soak, stage-record ticks, soak row

> **NOT AGENT WORK.** Owner checkpoint (09 §9.2.1). No agent may run `scripts/soak_record.py`, deploy to the
> reference host, read production, edit `ci/soak.txt`, `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, tick the
> stage record, decide Q-F, or merge a PR that changes those files. An agent handed this file stops and reports.

| Field | Value |
|---|---|
| ID | OWNER-08 |
| Tier | OWNER (no agent tier) |
| Wave | W07 (stage S2b, release `S2br2`, strict claims) |
| Depends on | PC-07-09 (merged) |
| Unblocks | W09 (S3): PC-09-01 onward, and the alias-table and deploy-file tasks that need P4 and Q-F |
| Duration | 14 days from `<since>` |
| Stage records | `docs/plans/platform-core/stage-ledger/S2b.md` (soak), `docs/plans/platform-core/stage-ledger/S3.md` (P4, Q-F) |

## Deploy and inventory

1. Deploy a `main` build at or after PC-07-09's merge commit with the committed `deploy` recipe
   (`scripts/soak_record.py --deployed` runs after its ready check).
2. P10 after each deploy, as in OWNER-07 step 6; commit `ci/prev_ref.txt` alone in a bookkeeping PR.
3. If strict mode refuses a write in production, dial back without a rollback: set
   `core.storage.claims.mode=shadow` in the operator layer and restart; the soak then restarts after the fix.

## After 14 days: the soak row

4. From the operator's checkout:

   ```bash
   python3 scripts/soak_record.py --release S2br2
   ```
   Pass criteria: as OWNER-07 step 7, with the claim criterion in **strict** mode: no persisted refusal whose
   `last_at` is at or after `<since>` (05-storage §5S.9.4 item 2).
5. Tick the stage record's done-check list; open a bookkeeping-only PR that changes `ci/soak.txt` alone.

## Pre-S3 owner reads and decisions (record in the S3 stage record)

6. **P4 env census** (09 §9.7) over every deploy form in use, names only (never values):

   ```bash
   ssh <host-a> 'systemctl show <unit> -p Environment | tr " " "\n" | cut -d= -f1'
   ssh <host-a> 'systemctl cat <unit> | grep -o "Environment=\"[A-Z_0-9]*"'
   docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' <container> | cut -d= -f1
   plutil -extract EnvironmentVariables xml1 -o - <plist> | grep -o '<key>[A-Z_0-9]*'
   docker compose config | grep -oE '^\s+[A-Z_0-9]+:'
   ```
   Pass: only bound or permanent names. Otherwise each implicit name in use gets an explicit alias row (and a
   permanent one if a deploy file sets it) before S3, and any value HEAD decodes differently from a strict decoder
   goes into `TestLegacyEnvDecodeMatchesHEAD`'s corpus (A-7).
7. **Decide Q-F** (14 §14.3): CLI flags and env names baked into shipped deploy files that are not the only
   source of a required setting: keep until core 1.0 (14's recommendation) or a two-minor window. Record the
   decision.
8. **Decide `OLLAMA_BASE_URL`** (09 S3 "Deploy files"; `deploy/local.conf.example:40` at the evidence commit): delete
   it from the shipped file, or replace it with the name ABO reads. Record the decision for PC-09-07.

## Done when

- `ci/soak.txt` holds a current `S2br2` row written by `scripts/soak_record.py --release S2br2`.
- The stage record ticks the stage's done-check list.
- P10 ran after each deploy and `ci/prev_ref.txt` is updated.
- P4's result, Q-F and the `OLLAMA_BASE_URL` decision are recorded in the S3 stage record.

## Planner notes

- **Same open read path** as OWNER-07: how `soak_record.py` reads the persisted strict-mode refusals is not
  specified before S4's core routes exist.
