<!-- file: docs/agent-tasks/dedup-label-quality/TASK-07-prod-remine-verify.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6b8871c5-55b0-46cf-84a4-5956fde5d9d6 -->
<!-- last-edited: 2026-07-10 -->

# TASK-07 — Prod re-mine + recalibrate + verify precision floor (INIT-1 T7) — ⛔ NOT AGENT WORK / operator-driven

**Gate:** SPEC -> GATED APPLY. Code PRs run autonomously (worktree/PR/CI). EVERY prod-data mutation (T7 rebuild-gold-labels re-mine, any calibration threshold/config apply, prod re-runs) is dry-run FIRST, then a real AskUserQuestion apply decision — never a text-reply approval.
**File-ownership:** no SOURCE code is touched — this task runs operations against production (172.16.2.30). It DOES write docs at close-out (step 9): a findings note under `.claude/notes/`, plus `TODO.md` and `CHANGELOG.md` updates. Those doc writes are committed via the repo's Quick Fix Workflow (branch + PR + rebase-merge; never direct to main) using the commit message block below.

**Priority:** P0 · **Effort:** M (operator time) · **Recommended subagent:** NONE — ⛔ NOT AGENT WORK: every apply step requires a real AskUserQuestion decision by the owner; an autonomous agent may at most prepare/present the dry-run evidence · **Depends on:** TASK-01, TASK-02, TASK-03, TASK-05 merged AND deployed to prod (`make deploy`). TASK-08 is a soft prerequisite: re-verify after it lands, but the first re-mine MAY run before it if INIT-2 is slow.

## ⛔ START HERE (context, not code)

This is a runbook, not a code task. No worktree, no PR. Prod access per the repo's standard path: `server-bootstrap` skill → `.claude/.api-token` → ops API on 172.16.2.30 (`Authorization: Bearer <abk_...>`). All ops are launched via `POST /api/v1/operations/v2 {"def_id": "..."}` and watched in the ops UI/API.

## Goal

Prove the whole initiative worked: after the label-mining fixes (TASK-01), duration normalization (TASK-02), and pair-dedup (TASK-03) are live on prod, re-derive the gold labels, re-run both calibrations, and confirm the precision floor that was unreachable on 2026-07-08 (best 0.582 @ cosine 0.98 vs a 0.90 low target) is now reachable — or produce the next evidence-backed diagnosis if it is not.

## Runbook (every apply is dry-run → AskUserQuestion → apply)

1. **Preflight (read-only).** Confirm the deployed binary contains the fixes: service healthy, and the ops registry lists `dedup.calibrate-composite`. ⛔ NOTE: `dedup.calibrate-composite` is the op ADDED BY TASK-05 — it does not exist anywhere in the repo (or the registry) until TASK-05 merges and deploys. Dependency gate: `grep -rn '"dedup.calibrate-composite"' internal/plugins/dedup/` against the deployed source must hit (>=1, in `calibrate_composite.go` + `plugin.go`); 0 hits = TASK-05 not merged/deployed → this runbook is BLOCKED at steps 5/6/8 — report "BLOCKED: TASK-05 unmerged" and stop (steps 2–4 alone may proceed only if the owner explicitly says so). Record baseline label stats via `GET /dedup/labels/stats` (rows, per-source, per-label counts).
2. **Re-mine — dry-run.** Run `dedup.rebuild-gold-labels` with NO apply param. Review the report: bucket deltas (rule/auto_high_conf re-derived; `human` passthrough count must equal the pre-run human count — the op preserves human rows by design, verify: `grep -n 'case "human"' internal/plugins/dedup/rebuild_gold_labels.go`). Expect the previously-contaminated `not_dup` population to shrink and `unsure` to grow (the TASK-01 downgrades).
3. **Re-mine — gated apply.** Present the dry-run diff to the owner via a REAL AskUserQuestion (never a text-reply approval). On approval, run with `{"apply":true}`. ⚠ **The apply is NON-ATOMIC:** it bulk-deletes all rule/auto_high_conf rows (`DeleteLabeledExamplesBySource`, rebuild_gold_labels.go:164) and then re-inserts fresh rows in a separate per-row loop (:171-178). Do not cancel/restart the service mid-apply; watch the op to completion and check its `write_errs` count. **A partial failure is NOT healed by re-running rebuild** — the rebuild diff derives fresh rows only from surviving labeled rows, so deleted-but-not-rewritten labels are invisible to it. Recovery from a partial apply: re-run the mining ops that re-derive labels from candidate state — `dedup.dataset-backfill` (rule rows) and `dedup.mine-gold-labels` (auto_high_conf rows) — then re-run rebuild. Human rows are safe throughout (passthrough; never deleted). Note the pre-apply JSONL export (`GET /dedup/labels/export`) is NOT a restore path — no import op exists; take it for the record only. Re-check `GET /dedup/labels/stats`: human count unchanged; not_dup count reduced.
4. **Embedding recalibration (read-only).** Re-run `dedup.calibrate-embedding-thresholds` `{model: bge-m3, target_precision: 0.98}`. Success criterion: `high` and/or `low` band reaches its target (no more `no_threshold_met_target=true` with best_p=0.582). Also confirm the new `rows_in`/`pairs_out` fields show the ~2.7× collapse (pairs_out ≈ unique pairs).
5. **Composite calibration (read-only).** Run `dedup.calibrate-composite` (defaults). Review per-band precision/recall vs the 2026-07-08 baseline; expect the CERTAIN/HIGH tiers to be servable by the composite even where a single cosine still is not (the ~47% recall tail).
6. **Optional composite apply.** ONLY if the report recommends values with all targets met: second REAL AskUserQuestion; on approval re-run with `{"apply":true}`. Record the previous `dedup.signals.*` values from the report (they are the rollback record).
7. **Verify downstream.** Re-run `dedup.full-scan` if candidate re-scoring is wanted. ⚠ full-scan has NO dry-run mode — it writes candidates/scores directly. Verify with a positive anchor: `grep -n 'func (p \*Plugin) runFullScan' internal/plugins/dedup/full_scan.go` must hit exactly one line reading `func (p *Plugin) runFullScan(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error` — the params argument is the discarded blank `_`, i.e. the op parses NO parameters at all, so no apply/preview/dry-run knob can exist. The write is idempotent/re-runnable, not irreversible, but it is still a prod-data mutation: gate it on an explicit operator AskUserQuestion consent BEFORE launching (there is no dry-run gate to lean on), or defer it out of this runbook; note the decision either way.
8. **Post-TASK-08 re-verify (soft).** After the engine ratio alignment lands and deploys, repeat steps 4–5 once — the veto narrowing can shift candidate emission slightly.
9. **Close out.** Write findings to `.claude/notes/2026-07-XX-dedup-remine-recalibration-findings.md` (substitute the run date). Copy the structure of the existing `.claude/notes/2026-07-08-dedup-calibration-findings.md`; if that file is unavailable, use this section skeleton: `# Title` / `## Summary` (3-5 bullets, outcome first) / `## Baseline stats` (pre-run label counts per source/label) / `## Dry-run diff` (bucket deltas from step 2) / `## Apply result` (op ID, write_errs, post-apply stats) / `## Calibration results` (embedding + composite, baseline vs new, targets met y/n) / `## Next steps`. Update `TODO.md`/`CHANGELOG.md`. Commit all three via the Quick Fix Workflow (branch + PR, commit message below). Report with exact counts (COMPLETED/REMAINING/BLOCKED).

## Acceptance criteria

- [ ] Re-mine apply ran to COMPLETION (op finished; `write_errs` 0 or each error accounted for — the apply is non-atomic, so an interrupted run means executing the partial-failure recovery in step 3 before proceeding)
- [ ] Re-mine applied with human-row count preserved exactly (stats before == after for `source=human`)
- [ ] `not_dup` label count decreased and `unsure` increased vs the pre-run stats (the TASK-01 effect, exact numbers recorded)
- [ ] Embedding calibration no longer reports best_p=0.582-class failure: at least one band meets target, OR a written finding explains why not (with the new diagnostics)
- [ ] Composite calibration report recorded (baseline vs recommended); any apply was AskUserQuestion-gated with previous values recorded
- [ ] Findings note written; TODO.md/CHANGELOG.md status explicitly reported
- [ ] Anti-over-suppression: N/A — this task runs existing prod ops and calibration; it adds no new filter/guard/veto/skip/dedupe code path (step 8 merely RE-MEASURES after TASK-08's veto change lands elsewhere)

## Commit message (step-9 doc writes only)

Via the Quick Fix Workflow (branch + PR + `gh pr merge --rebase`; never direct to main):

```
docs(dedup): record TASK-07 prod re-mine + recalibration findings (INIT-1 T7)

Findings note under .claude/notes/, TODO.md and CHANGELOG.md updated with
the re-mine apply result, embedding + composite calibration outcomes, and
the precision-floor verdict vs the 2026-07-08 baseline.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## Idempotency / Rollback

Re-runnable on COMPLETED runs: `dedup.rebuild-gold-labels` apply is a stable no-op on unchanged state and never deletes `human` rows, so a bad-but-complete re-mine (logic error) is corrected by re-running after a further rule fix. ⚠ It is NOT self-healing against PARTIAL failure: the apply is non-atomic (bulk delete → per-row re-insert), and a run interrupted between the two loses rule/auto_high_conf rows that a rebuild re-run cannot regenerate (the diff only sees surviving rows). Partial-failure recovery = re-run `dedup.dataset-backfill` (re-derives rule labels from candidate state) and `dedup.mine-gold-labels` (auto_high_conf), then rebuild. The JSONL export is documentation, not a backup — no import op exists. No config is written unless a calibration explicitly recommends it AND the owner approves; an applied composite config is rolled back by a second gated apply restoring the previous `dedup.signals.*` values echoed in the op report (it persists in the settings store and survives redeploy — reverting code does not revert it). No code rollback exists or is needed — this task ships no code.
