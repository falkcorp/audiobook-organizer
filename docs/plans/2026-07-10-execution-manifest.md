<!-- file: docs/plans/2026-07-10-execution-manifest.md -->
<!-- version: 1.0.0 -->
<!-- guid: 11f6e9cc-9c04-4da8-89aa-090f0408cb42 -->
<!-- last-edited: 2026-07-10 -->

# Execution Manifest — 2026-07-10 remaining-work planning packages

**Audience:** the Opus + ultracode orchestrator that executes these packages. This is the
single entry point: per initiative — spec, plan, task package, gate, dependencies, and the
recommended execution order. Source catalog: `.claude/notes/2026-07-10-remaining-work-master-plan.md`
(gitignored, main checkout only). Every file:line anchor cited in these packages was
grep-verified at HEAD `fce58498` on 2026-07-10; each package survived a 3-lens adversarial
design-judge panel (correctness / ops-rollback / simplicity-scope), per-brief cold-executor
verification, a repair loop, and a mechanical audit before landing.

## Package inventory

| # | Initiative | Spec | Plan | Tasks | Gate |
|---|-----------|------|------|-------|------|
| INIT-1 | Dedup label-quality & refinement loop | [spec](../specs/2026-07-10-dedup-label-quality-design.md) | [plan](2026-07-10-dedup-label-quality.md) | [8 briefs](../agent-tasks/dedup-label-quality/) | SPEC → GATED APPLY (every prod-data mutation: dry-run → AskUserQuestion) |
| INIT-2 | Dedup pipeline hardening | [spec](../specs/2026-07-10-dedup-pipeline-hardening-design.md) | [plan](2026-07-10-dedup-pipeline-hardening.md) | [6 briefs](../agent-tasks/dedup-pipeline-hardening/) | PLAN → EXECUTE autonomously; T3/T6 prod drains dry-run → AskUserQuestion |
| INIT-3 | Metadata matching pipeline | [spec](../specs/2026-07-10-metadata-matching-design.md) | [plan](2026-07-10-metadata-matching.md) | [8 briefs](../agent-tasks/metadata-matching/) | PLAN → EXECUTE autonomously; config extraction defaults = today's literals |
| INIT-4 | Filtering & search pipeline | [spec](../specs/2026-07-10-filtering-search-design.md) | [plan](2026-07-10-filtering-search.md) | [6 briefs](../agent-tasks/filtering-search/) | PLAN → EXECUTE autonomously; T1/T2 correctness fixes ship first |
| INIT-5 | Torrent client-agnostic relocation | [spec](../specs/2026-07-10-torrent-relocation-design.md) | [plan](2026-07-10-torrent-relocation.md) | [7 briefs](../agent-tasks/torrent-relocation/) | SPEC → EXECUTE; **T2 = real-Deluge spike, STOP-FOR-HUMAN sign-off before T3** |
| INIT-6 | Pluggable Workflow System (WF-2..6) | [spec](../specs/2026-07-10-workflow-system-design.md) | [plan (stub)](2026-07-10-workflow-system.md) | [AWAIT-APPROVAL](../agent-tasks/workflow-system/AWAIT-APPROVAL.md) | **STOP-FOR-HUMAN** — spec-only, core-infra blast radius |
| INIT-7 | OpenAI Responses API migration | [spec](../specs/2026-07-10-responses-api-migration-design.md) | [plan](2026-07-10-responses-api-migration.md) | [HOLD-STATUS](../agent-tasks/responses-api-migration/HOLD-STATUS.md) | **CONFIRM-HOLD** — blocked on #1260–#1265 hold-lift |
| INIT-8 | Community fingerprint index | [spec](../specs/2026-07-10-community-fingerprint-index-design.md) | [plan (stub)](2026-07-10-community-fingerprint-index.md) | [AWAIT-APPROVAL](../agent-tasks/community-fingerprint-index/AWAIT-APPROVAL.md) | **STOP-FOR-HUMAN** — spec-only, new-product blast radius |
| INIT-9 | Bug + tech-debt cluster | [spec](../specs/2026-07-10-bug-techdebt-design.md) | [plan](2026-07-10-bug-techdebt.md) | [7 briefs](../agent-tasks/bug-techdebt/) | PLAN → EXECUTE autonomously; **TASK-06 REPO-SIZE-1 = STOP-FOR-HUMAN plan-only** |
| INIT-10 | Small UX/feature items | [spec](../specs/2026-07-10-ux-small-items-design.md) | [plan](2026-07-10-ux-small-items.md) | [8 briefs](../agent-tasks/ux-small-items/) | PLAN → EXECUTE where cheap; C8 issue-filing dry-run → AskUserQuestion |

50 executable TASK briefs total (INIT-6/7/8 carry stop/hold stubs instead).

## Cross-initiative constraints (hard — never violate)

1. **`internal/dedup/engine.go` partition:** INIT-2 owns all structural edits
   (its TASK-03 + TASK-05). Wait for INIT-2's engine.go waves to MERGE before dispatching:
   - INIT-1 TASK-08 (ratio 0.6→0.5 alignment)
   - INIT-4 TASK-05 (boilerplate blocklist extraction)
   Never schedule any two of {INIT-2 engine tasks, INIT-1 T08, INIT-4 T05} concurrently.
2. **`internal/server/metadata_ops.go`:** INIT-3 TASK-05 (bulk-fetch parallelization) and
   INIT-10 TASK-07 (SLOG-W13 sweep, wires `runBulk*` ops) may both touch it — serialize
   these two briefs (either order; rebase the second).
3. **Store-getter twins:** INIT-2 T1/T2 must ship Pebble + MemStore twins together and run
   the full `go test ./... -short` (stamped in the briefs).
4. **INIT-9 does not duplicate INIT-2:** the two stub getters are INIT-2 T1/T2; INIT-9 only
   cross-references.
5. **Prod-data mutations** (INIT-1 T7 re-mine/calibration applies; INIT-2 T3/T6 drains;
   INIT-10 C8 issue filing): dry-run first, then a REAL AskUserQuestion decision. A
   text-reply approval does not count (see memory `feedback_prod_apply_review_gate`).
6. **C8 target repo:** auto-filed not_dup-cluster issues go to `falkcorp/burndown-tasks`
   (precedent: burndown-tasks #52–#67; TODO.md:826), never this repo. Burndown bot
   workflows are paused since 2026-07-08 — filing works, auto-triage resumes when re-enabled.

## Recommended execution order

**Phase A (parallel, autonomous):**
- INIT-2 waves W1→W3 (highest leverage: revives dead tiers, kills 387k explosion; unblocks the partition)
- INIT-1 waves W1–W3 (rules/dedup/duration/queue/calibration — no engine.go)
- INIT-4 T1/T2 (user-visible correctness), then T3/T4/T6
- INIT-3 all waves (respect constraint 2 vs INIT-10 T07)
- INIT-9 all except TASK-06 (REPO-SIZE-1 plan doc may be *written* anytime; it STOPs at the plan)
- INIT-10 T01–T04, T06–T08 (respect constraint 2)
- INIT-5 TASK-01 (interface, additive) + TASK-02 spike prep

**Phase B (after Phase-A merges):**
- INIT-1 TASK-08 + INIT-4 TASK-05 (rebase on INIT-2's merged engine.go)
- INIT-1 T7 prod re-mine + recalibration (operator runbook, AskUserQuestion-gated)
- INIT-2 T6 prod drain (after T3 deploys; AskUserQuestion-gated)
- INIT-10 TASK-05 (C8) — after INIT-1 T7 delivers clean labels

**Phase C (human decision points — present, then STOP):**
- INIT-5 T2 spike results → human sign-off → then T3–T7
- INIT-6 spec review (workflow system)
- INIT-8 spec review (community index)
- INIT-9 TASK-06 REPO-SIZE-1 migration plan review
- INIT-7 hold-lift confirmation (#1260–#1265)

## Appendix — out-of-catalog open TODO items (surveyed 2026-07-10, deliberately excluded)

All 46 open TODO.md checkboxes were swept; 34 map into INIT-1..10. The remaining 12 were
left out of the catalog on purpose — dispositions:

| Item | TODO.md | Disposition |
|---|---|---|
| PH-2 residual-triage prod run | :411 | operational — candidate to fold into INIT-2's drain lane (T6 runbook) |
| CONS-17b non-generic-tag residual | :494 | small dedup-import residual — INIT-2-adjacent, needs its own scoping |
| Whole-file fp migration verifies ×2 | :860–861 | [hold] prod verification only |
| MDA3 subprocess isolate revert | :912 | [hold] gated on MDA1+MDA2 CI |
| PD-1 / MAYDEPLOY-A revisit | :1031 | [hold] |
| PD-3 post-deploy verification | :1045 | operational checklist (docs/pd3-prod-verification.md) |
| I1 + I6 heap-audit verifies (+2 dupes) | :992,:1015,:1067,:1070 | [hold] prod pprof measurements |
| SEC-AUDIT-11 closeout | :1637 | operational dismissal pass |
| Backlog 1.17 branding, 3.8 Plex API, 3.9/3.10, 4.1/4.7/4.10 | :2117–2153 | long-horizon backlog, mostly [hold] |

## Provenance

- Anchors: 58 master-plan anchors re-verified at `fce58498`; 14 drifted/missing were
  corrected before drafting (biggest: `service_query.go` → `internal/audiobooks/`;
  INIT-7 spec link dangling — real briefs at `docs/agent-tasks/ai-responses-migration/`;
  ratings RATE-1..5 already shipped).
- Adversarial review: 30 design-judge verdicts (3 lenses × 10), 20 blockers + 50 majors +
  95 minors found → 134 fixes applied, 21 rejected with recorded reasons.
- Brief verification: 50 briefs role-played by cold-executor verifiers; 26 flagged →
  repaired; residuals re-checked by the mechanical audit.
- Some briefs carry *designed* zero-hit STOP preconditions (they depend on an earlier
  unmerged task, e.g. INIT-1 TASK-05/06 on TASK-03/05). Executors must honor the STOP
  branch, not treat it as drift.
