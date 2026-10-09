<!-- file: docs/proposals/2026-10-holistic/tasks/00-TEMPLATE.md -->
<!-- version: 1.1.0 -->
<!-- guid: 5c1d9e2f-3b7a-4d86-9f0e-2a6c4b8d1e37 -->
<!-- last-edited: 2026-10-09 -->

# Task brief template

One file per PR, named `<workstream>-<pr-id>.md` (for example `01-P1.md`, `07-C1.md`, `02-PR5a.md`). A brief must be executable by a subagent that has **no conversation context**: everything it needs is in the brief or in a file the brief names by path.

```markdown
<!-- file: docs/proposals/2026-10-holistic/tasks/<ws>/<ws>-<id>.md -->
<!-- version: 1.0.0 -->
<!-- guid: <uuid> -->
<!-- last-edited: 2026-10-09 -->

# <ws>-<id>: <one-line title>

| Field | Value |
|---|---|
| Model | sonnet (default) or opus (only when the task needs cross-package judgment, a new runtime invariant, or a data migration) |
| Wave | 0 / 1 / 2 / F / 3 / 4 |
| Size | S / M / L |
| Depends on | `**Merge first:** <comma-separated brief ids, or none>.` then prose (why; rebase-order notes; ids that are wave 3/4 and not briefed). Only the Merge first list is authoritative; `scripts/check_task_briefs.py` verifies it (ids exist, waves respect it, no cycles) |
| Blocks | generated: run `scripts/check_task_briefs.py --regen-blocks --index` after editing any Merge first list or Wave; never hand-edit |
| Owner decisions | D-numbers from 09-owner-decisions.md that this task implements |
| Spec | the section of the proposal doc this task comes from, by path and heading |

## Goal
Two to four sentences. What is true after the PR merges that is not true now.

## Context the agent must read first
Bullet list of paths (and line ranges where useful). Keep it to what is needed.

## Files
| Path | Change |
|---|---|
| exact path | create / edit (what) / delete |

## Steps
Numbered, concrete, in order. Name functions and symbols. Say what must NOT change.

## Tests
- Which existing tests must stay green (package paths).
- New tests to add, by file and case name.
- The exact commands: `go test ./internal/...`, `cd web && npx vitest run <path>`, `npx tsc --noEmit -p web`, `make ci` where required.

## Acceptance
Checklist a reviewer can verify without reading the diff (counts, commands with expected output, UI names).

## Rollback
How to undo if it misbehaves in production (revert commit; flag; data restore).

## Guardrails
- Standing bans that apply (copy the relevant lines from `00-charter.md` §6).
- Worktree rule: `git worktree add ../aorg-<id> -b <branch> origin/main`; never edit main.
- File headers: bump version and last-edited on every touched file; changelog fragment in `changelog.d/` with no header.
- Commit: conventional commit; explicit `git add` of named files only.

## Report format
What to put in the hand-back: files changed, test output lines, counts, anything unverified.
```

## Model guidance

- **sonnet**: mechanical edits with a complete file list, deletions proven by build, config flips, test ports, UI wiring that follows an existing pattern, metric additions, doc edits.
- **opus**: new runtime invariants (the ops v3 fence, chunk-leasing runner, readiness gating), storage migrations and cut-overs, scoring/calibration logic, anything whose file list is "discover and sweep".

The brief writer decides; the coordinator may override in the wave index.
