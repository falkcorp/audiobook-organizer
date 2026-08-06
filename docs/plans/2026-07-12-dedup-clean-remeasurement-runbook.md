<!-- file: docs/plans/2026-07-12-dedup-clean-remeasurement-runbook.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9a4d1c76-2e83-4f51-b0a7-6c3e8d29f1b5 -->
<!-- last-edited: 2026-07-12 -->

# Dedup clean re-measurement runbook (INIT-1 T7 close-out) — 2026-07-12

**Purpose.** Produce the TRUE post-guard dedup precision number and unblock the composite
calibration, from a *clean* candidate/label state. Do this as ONE deliberate operator pass —
not reactive one-off ops. Every prod-data mutation below is dry-run → real decision → apply.

## Why this is needed
Over 2026-07-12 the labels/candidates went through: a stale-binary re-mine (invalid), a correct
re-mine, `dataset-backfill`+`mine-gold-labels` applies, a `drain-stale`, and the same-title guard
deploy (PR #1922). The state is churned, so re-running `dataset-backfill` now is a partial no-op
(candidates already suppressed) and can't measure the guard's effect. Also the composite scorer
can't calibrate: only ~234 of 2,474 labeled pairs carry a `ScoreBreakdown`
(`insufficient-coverage`). A fresh `dedup.full-scan` rebuilds candidate scores → fixes both.

## Preconditions (verify first)
```bash
# 1. Fresh API key (server-bootstrap skill → .claude/.api-token). Key: Authorization: Bearer abk_...
KEY=$(grep '^api_key=' .claude/.api-token | cut -d= -f2)
BASE=https://<server>:8484/api/v1
# 2. Correct binary deployed (guard live). MUST show calibrate-composite:
curl -sk "$BASE/op-defs" -H "Authorization: Bearer $KEY" | grep -o 'dedup.calibrate-composite' | head -1
#    If empty: LOCAL tree is stale. git pull --ff-only origin main && make deploy   (see
#    memory feedback_make_deploy_builds_local_tree — make deploy builds the LOCAL tree).
```

## API mechanics (learned 2026-07-12 — do not repeat the mistakes)
- Launch: `POST $BASE/operations/v2` body `{"def_id":"...","params":{"apply":<bool>}}`.
  **`apply` MUST be nested under `params`** — a top-level `apply` is silently ignored and the op
  runs as a DRY-RUN. Always confirm the op's start log shows the apply value you intended.
- Poll: `GET $BASE/operations/v2/<op_id>` → `.data.logs[]`; done when a log line starts
  `operation finished`. The report is in the last 2-3 log lines.
- Label stats anytime: `GET $BASE/dedup/labels/stats`.

## Step 0 — baseline snapshot (read-only, for the record)
```bash
curl -sk "$BASE/dedup/labels/stats" -H "Authorization: Bearer $KEY"      # record counts
curl -sk "$BASE/dedup/labels/export" -H "Authorization: Bearer $KEY" > /tmp/labels-pre-$(date +%s).jsonl
```
NOTE: the export is documentation only — there is NO import op, so it is not a restore path.

## Step 1 — `dedup.full-scan` (⚠ NO dry-run — writes candidates/scores directly)
This op parses NO params (the write is idempotent/re-runnable but IS a prod mutation). Gate it on
an explicit operator OK — there is no dry-run to lean on. It rebuilds the candidate set and,
crucially, populates `ScoreBreakdown` for pairs (what the composite calibration needs).
```bash
op=$(curl -sk -X POST "$BASE/operations/v2" -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"def_id":"dedup.full-scan"}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["op_id"])')
# poll until "operation finished"; full-scan over the library can take minutes.
```

## Step 2 — `dedup.dataset-backfill` (dry-run → decision → apply)
Regenerates rule labels from the fresh candidate state WITH the same-title guard active.
```bash
# dry-run — expect not_dup to drop vs pre-guard (the ~267 same-title mislabels → unsure),
# boilerplate "Big Finish Ident" pairs KEPT not_dup (carve-out), unsure to grow.
curl ... -d '{"def_id":"dedup.dataset-backfill","params":{"apply":false}}'
# REAL operator decision on the dry-run diff, then:
curl ... -d '{"def_id":"dedup.dataset-backfill","params":{"apply":true}}'
```

## Step 3 — `dedup.mine-gold-labels` (dry-run → apply)
Auto-labels high-confidence `true_dup` positives (idempotent upsert).
```bash
curl ... -d '{"def_id":"dedup.mine-gold-labels","params":{"apply":false}}'   # then apply:true
```

## Step 4 — calibrations (read-only — THE measurement)
```bash
# embedding: the headline precision number
curl ... -d '{"def_id":"dedup.calibrate-embedding-thresholds","params":{"model":"bge-m3","target_precision":0.98}}'
# composite: now has ScoreBreakdown coverage → should no longer be insufficient-coverage
curl ... -d '{"def_id":"dedup.calibrate-composite","params":{}}'
```
Read `high_best_precision_achieved` / `low_best_precision_achieved` and `no_threshold_met_target`.
**Success = best precision clears the 0.90 low target** (baseline was 0.582 on 2026-07-08, 0.606
post-re-mine pre-guard on 2026-07-12). Capture the top not_dup sample pairs the embedding op logs —
if same-title cosine-1.0 pairs are GONE, the guard worked; if a NEW class dominates, that's the
next lever.

## Step 5 — optional threshold/band apply (gated)
Only if a calibration recommends values with ALL targets met: a SECOND real operator decision,
then re-run with `{"params":{"apply":true}}`. Record the previous `dedup.signals.*` values from the
report — they are the rollback record (persisted in settings; survives redeploy; reverting code
does NOT revert them).

## Step 6 — close out
Update `.claude/notes/2026-07-12-dedup-remine-recalibration-findings.md` with the clean numbers,
then TODO.md/CHANGELOG.md via the Quick Fix Workflow (branch + PR, never direct to main).

## Safety / rollback
- Human labels are passthrough in every op above — never deleted. Verify `source=human` count is
  unchanged before/after.
- `dataset-backfill`/`mine-gold-labels` use idempotent `UpsertLabeledExample` — re-runnable.
- `full-scan` is re-runnable (idempotent writes), not restore-able; the JSONL export is not a backup.
- The same-title guard can only downgrade `not_dup → unsure` (a review-queue holding state) — it
  can never cause a wrong merge, so even an over-broad downgrade is recoverable by relabeling.
