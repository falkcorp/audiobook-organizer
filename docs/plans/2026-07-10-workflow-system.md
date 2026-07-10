<!-- file: docs/plans/2026-07-10-workflow-system.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8233e0fe-4f62-420f-ab7e-8317a5640194 -->
<!-- last-edited: 2026-07-10 -->

# Pluggable Workflow System (WF-2..WF-6) Implementation Plan — STUB

> **GATE (verbatim):** STOP-FOR-HUMAN. Spec-only initiative: core-infra blast radius. NO code, NO task briefs, NO execution until a human approves the spec. The only 'task' is AWAIT-APPROVAL.

**Goal:** none executable. This initiative's entire deliverable is the design-options spec at
`docs/specs/2026-07-10-workflow-system-design.md` (Status: Draft — STOP-FOR-HUMAN review required).

**Spec:** `docs/specs/2026-07-10-workflow-system-design.md`. No milestone of that spec is
authorized. An implementation plan (TDD, milestone-per-wave) will be authored as a NEW plan-op
package only after the human approves the spec and resolves its open questions.

---

## Status

The spec awaits human review. There are NO execution steps in this plan. Do not dispatch agents.

## Task skeleton

| Task | exact_files | depends_on | polarity | priority | wave |
|---|---|---|---|---|---|
| AWAIT-APPROVAL | none (no code) | human review of the spec | n/a | P1 | 0 |

**Wave 0 — Execution mode: NOT AGENT WORK** (numeric trigger: 0 executable tasks, 0 files owned —
below every dispatch threshold; the only action is a human decision). Collision matrix: empty
(no exact_files anywhere).

**File-ownership:** n/a (no code).

## Decision points the human must resolve (mirrors the spec's "Open questions")

1. Axis picks: WF-2 capability mechanism (1A/1B/1C), WF-3 Workflow shape (2A/2B/2C), WF-4
   enforcement (3A/3B/3C), WF-5 UI form (4A/4B). Recommendations on file: 1B, 2A, 3A, 4A.
2. Capability-unmet default policy: `skip` vs `park` (esp. Ollama-gated embedding ops).
3. Authoritative surface for the enable bit during the legacy-config compat window.
4. Whether `dedup_embeddings_enabled` truly collapses into workflow membership or stays a flag.
5. If 2B is picked: accept the core-scheduler surgery to let empty-subject ops be required-on.
6. Workflow-run concurrency semantics (proposed: singleton per workflow ID).
7. INIT-1 T6 sequencing: ship as plain op chain now, convert at WF-3 seeding (proposed), or hold.
8. Confirm "nothing runs unless in an enabled workflow" is a v1 non-goal.
9. Whether collapsing the eight `scheduled_*` families alone justifies the Workflow object for
   v1, vs the simpler "keep `scheduled_*` + T6 as a plain op chain" (which defers WF-3).

Note: decision 3 (shim write authority) is an **M2 entry gate** — it must resolve to a single
authoritative writer **with a surfaced forward-write failure mode** (a failed proxy write must
error to the operator, never silently report a disable that didn't land) before any shim is
seeded, per the spec.

## After approval

Re-invoke plan-op against the approved spec to produce the real implementation plan + task
briefs (expected shape: M1 WF-2, M2 WF-3 flag-off, M3 cutover, M4 WF-4, M5 WF-5 — serial waves,
core-infra files are single-owner). That plan MUST carry forward two ownership constraints:
(a) the following files are **single-owner** in its collision matrix —
`internal/operations/registry/types.go`; `internal/operations/registry/deps_scheduler.go` (the
park/wake requirement evaluator Axis-1 option 1B touches); and **`internal/scheduler/tasks.go`**
(23 `Scheduled.*` references — the highest-collision surface: it is where WF-3's cutover
arbiter lands AND where INIT-1 T6's plain op chain would be wired; note there is NO `engine.go`
under `internal/operations/` — the ownership partition is over `internal/scheduler/tasks.go`);
(b) **no INIT-1 T6 wave may run concurrently with the WF-2/WF-3 waves** — both touch
`internal/scheduler/tasks.go`, and T6's op chain converts to a seeded builtin at WF-3
seeding, so they must be serialized. Until then: STOP.
