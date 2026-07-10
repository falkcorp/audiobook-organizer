<!-- file: docs/agent-tasks/dedup-label-quality/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 71d5865c-8ead-45f7-b5fa-c8a15e94e30b -->
<!-- last-edited: 2026-07-10 -->

# Workstream — Dedup Label-Quality & Training/Refinement Loop (INIT-1)

Fix the contaminated `not_dup` gold labels at the mining source, dedupe label consumption per book-pair, grow human label volume via a suspicious-label review queue, calibrate the noisy-OR composite scorer on the clean set, and add a disabled-by-default scheduled refinement loop — then re-mine + recalibrate on prod and confirm the 2026-07-08 precision floor (0.582 @ cos 0.98) becomes reachable. From master plan `.claude/notes/2026-07-10-remaining-work-master-plan.md` §INIT-1 and `.claude/notes/2026-07-08-dedup-calibration-findings.md`. Spec: `docs/specs/2026-07-10-dedup-label-quality-design.md` · Plan: `docs/plans/2026-07-10-dedup-label-quality.md`.

**Gate (verbatim, every task):** SPEC -> GATED APPLY. Code PRs run autonomously (worktree/PR/CI). EVERY prod-data mutation (T7 rebuild-gold-labels re-mine, any calibration threshold/config apply, prod re-runs) is dry-run FIRST, then a real AskUserQuestion apply decision — never a text-reply approval.

| Task | Source id | Title | Priority | Effort | Tier | Wave |
|------|-----------|-------|----------|--------|------|------|
| TASK-01 | INIT-1 T1 | Guard the not_dup mining rules (identity guard; missingFile → unsure) ⚠ | P0 | M | Sonnet-class | 1 |
| TASK-02 | INIT-1 T2 | Duration ms/sec normalization, per-file at the mining boundary (shipped CONS-18 write repair verified, not re-added) | P0 | S | Sonnet-class | 2 |
| TASK-03 | INIT-1 T3 | Pair-dedup labeled-example consumption (2.7× collapse) | P1 | M | Sonnet-class | 1 |
| TASK-04 | INIT-1 T4 | Suspicious-label review queue + one-click override UI | P1 | M | Sonnet-class | 2 |
| TASK-05 | INIT-1 T5 | Composite (noisy-OR) calibration op — report + gated apply ⚠ | P1 | L | Opus/strong-class | 2 |
| TASK-06 | INIT-1 T6 | Scheduled refinement loop (built-in-DISABLED, WF-3-aligned) | P2 | S | Sonnet-class | 3 |
| TASK-08 | INIT-1 (ownership note) | Engine part-vs-whole ratio align 0.6 → 0.5 | P2 | S | Sonnet-class | 4 (post-INIT-2) |
| TASK-07 | INIT-1 T7 | Prod re-mine + recalibrate + verify — ⛔ NOT AGENT WORK | P0 | M | operator | 5 |

## Ground rules

- Go backend (`internal/…`) + one React page (`web/src/pages/DedupLabels.tsx`, TASK-04 only). Briefs are **standalone mode**: each task = its own worktree + branch + PR + `gh pr merge <n> --rebase` (rebase/FF only; NEVER commit to main).
- Build + test gate for every task in this workstream:
  ```bash
  make ci
  ```
  staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee; a 0-hit grep means STOP and report.
- File version headers bumped on every touched file; conventional commits; whole-library loops get bounded worker pools (`errgroup+SetLimit` — relevant to TASK-05's sweep); run the FULL `go test ./... -short` whenever a shared type/package changes (TASK-01/02/03).
- **File-ownership (verbatim):** INIT-2 OWNS all structural edits to `internal/dedup/engine.go`. INIT-1's single engine.go touch (TASK-08) lands AFTER INIT-2's engine.go waves merge, rebased on top — never a concurrent wave on engine.go. `ListLabeledExamples` is implemented in `internal/database/dedup_label.go` (NOT embedding_store.go), so TASK-03 does not collide with INIT-2's embedding_store.go index work. (This corrects the master-plan §INIT-1 premise that placed it in embedding_store.go; INIT-2 also touches neither `dedup_label.go` nor `internal/plugins/dedup/plugin.go`, so TASK-01's BookFeatures edit and TASK-05's op registration are collision-free.)

## Collision / wave note

**TASK-01 and TASK-02 both edit `internal/dedup/dataset/builder.go` + `builder_test.go`; TASK-03 and TASK-04 both edit `internal/server/handlers/dedup/label_review.go`.** Each pair MUST run in different waves (TASK-02 serialized after TASK-01 merges; TASK-04 after TASK-03) — running them in parallel would produce a same-file merge conflict on every rebase cycle. TASK-01+TASK-03 touch disjoint files and run in wave 1 concurrently; TASK-02+TASK-04+TASK-05 are disjoint and run in wave 2 (TASK-05 additionally needs TASK-03's `DedupeByPair`); TASK-06 (wave 3) needs TASK-05's op ID; TASK-08 (wave 4) is externally serialized behind INIT-2's engine.go waves; TASK-07 (wave 5) is the operator runbook after waves 1–2 are deployed.

Wave table:

| Wave | Tasks | Prereq | Parallel-safe because |
|---|---|---|---|
| 1 | TASK-01, TASK-03 | none | disjoint file sets (see collision table in the plan) |
| 2 | TASK-02, TASK-04, TASK-05 | wave 1 merged + siblings rebased | disjoint within wave; serializations vs wave 1 satisfied |
| 3 | TASK-06 | TASK-05 merged | single task |
| 4 | TASK-08 | EXTERNAL: INIT-2 engine.go waves merged | single task, rebased on top |
| 5 | TASK-07 | waves 1–2 merged + `make deploy`; TASK-08 soft | NOT AGENT WORK — operator runbook, AskUserQuestion-gated applies |

Execution modes (from the plan, authoritative): W1 = SINGLE-AGENT (strong model) per task, parallel-safe (2 heterogeneous tasks, under the ≥3 /parallel-sweep threshold); W2 = /parallel-sweep (3 tasks ≥3 threshold, disjoint files, gate `make ci`); W3/W4 = SINGLE-AGENT; W5 = NOT AGENT WORK.

The coordinator + worker protocol is embedded verbatim in `docs/plans/2026-07-10-dedup-label-quality.md` §Coordinator protocol (there is no ORCHESTRATION.md one level up for this package; the plan section is authoritative). When dispatching briefs standalone, the brief's own PR + merge section applies.
