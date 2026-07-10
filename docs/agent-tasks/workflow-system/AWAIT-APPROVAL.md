<!-- file: docs/agent-tasks/workflow-system/AWAIT-APPROVAL.md -->
<!-- version: 1.0.0 -->
<!-- guid: d1f3260d-9970-4dad-a30b-1781ac01afd6 -->
<!-- last-edited: 2026-07-10 -->

# AWAIT-APPROVAL — Pluggable Workflow System (WF-2..WF-6)

**Gate:** STOP-FOR-HUMAN. Spec-only initiative: core-infra blast radius. NO code, NO task briefs, NO execution until a human approves the spec. The only 'task' is AWAIT-APPROVAL.
**File-ownership:** n/a (no code)

This initiative has no executable tasks until the spec is approved by the owner. Do not dispatch
agents.

- Spec under review: `docs/specs/2026-07-10-workflow-system-design.md`
  (Status: Draft — STOP-FOR-HUMAN review required)
- Plan stub: `docs/plans/2026-07-10-workflow-system.md` — lists the 9 decision points the human
  must resolve.
- After approval: author a NEW plan-op package (implementation plan + briefs) from the approved
  spec. Nothing in this directory authorizes execution.
