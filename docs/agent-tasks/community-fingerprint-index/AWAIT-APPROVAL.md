<!-- file: docs/agent-tasks/community-fingerprint-index/AWAIT-APPROVAL.md -->
<!-- version: 1.0.0 -->
<!-- guid: e4103a3b-002b-4c66-b2c8-93d747615a8e -->
<!-- last-edited: 2026-07-10 -->

# AWAIT-APPROVAL — Community Audiobook Acoustic-Fingerprint Index (INIT-8)

**Gate:** STOP-FOR-HUMAN. New-product blast radius. Spec only; NO code, NO task briefs, NO repo creation, NO external publication until a human approves. The only 'task' is AWAIT-APPROVAL.
**File-ownership:** n/a (no code)

## DO NOT DISPATCH

This is **not** an executable agent task. No orchestrator, sweep coordinator, or subagent may
act on this initiative beyond confirming the spec exists. Specifically forbidden until a human
approves the spec:

- Writing ANY product code (organizer ops, validator CLI, workflows) for this initiative.
- Creating the external GitHub repo or any repo/org resource.
- Publishing, pushing, or PR-ing any library-derived fingerprint/metadata externally.
- Generating further TASK-NN briefs for this initiative.

## What the human is being asked to review

- Spec: `docs/specs/2026-07-10-community-fingerprint-index-design.md`
  (Status: Draft — STOP-FOR-HUMAN review required) — decisions D1–D6 + open questions OQ1–OQ5.
- Plan stub: `docs/plans/2026-07-10-community-fingerprint-index.md`.

Approval must be an explicit human decision (per the prod-apply review-gate rule, a real
AskUserQuestion / direct user instruction — a passing mention in chat does not count).
OQ5 (CC0 is irrevocable; publishing library-derived data) deserves an explicit yes.

## Idempotency / verification (polarity: presence — spec-only)

Run:

```bash
grep -n 'Status:.*Draft — STOP-FOR-HUMAN review required' docs/specs/2026-07-10-community-fingerprint-index-design.md
```

Expected: 1 match — the spec exists and is still gated. If the Status line has been flipped to
approved by a human, a NEW plan-op session builds the implementation package; this brief still
does not authorize any work.

Anchor re-verify (provenance for the spec's citations — run before quoting it anywhere):

Run:

```bash
grep -n 'func SynthesizeBookSignature' internal/fingerprint/book_signature.go
grep -n 'dedup-tuning-dataset-design' TODO.md
grep -n 'Needs Serious Planning' TODO.md
```

Expected: each returns ≥1 match (book_signature.go:48, TODO.md references at ~:620 and ~:617 —
line numbers drift; trust the grep, not the numbers).
