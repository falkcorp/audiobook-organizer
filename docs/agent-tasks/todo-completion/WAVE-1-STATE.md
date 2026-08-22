<!-- file: docs/agent-tasks/todo-completion/WAVE-1-STATE.md -->
<!-- version: 1.1.0 -->
<!-- guid: 5b3c9e21-8f47-4a6d-b0c2-71e4d8a35f90 -->
<!-- last-edited: 2026-08-22 -->

# Wave 1 — execution state as of 2026-08-22 06:15 EDT

## Merged to main (12)

| Task | PR | What |
|---|---|---|
| TASK-017 | #2683 | `APIRateLimitPerMinute` default drift (fresh-install vs factory reset) |
| TASK-215 | #2684 | never send `batchFetchCandidates({})` from the Search provider (REV-EMPTY-1) |
| TASK-032 | #2685 | 4 missing compile-time interface assertions |
| TASK-053 | #2686 | delete the `/torrents` group-relative fragment from openapi.json |
| TASK-218 | #2687 | OperationActivityPanel: stop re-appending the last SSE log line (REV-EMPTY-4) |
| TASK-091 | #2691 | remove dead `expanded` state in TagComparison (+ `Book[]` fixture typing) |
| TASK-011 | #2692 | pin SHA256 checksums for Dockerfile-fetched utfcpp/taglib |
| TASK-221 | #2690 | metafetch: collapse duplicate `book_file` rows by cleaned path (DUPROW-1) |
| TASK-031 | #2693 | lock the three bare `globalStore` accesses |
| TASK-088 | #2696 | route lsh-backfill's `lshIndexChecker` through `database.AsCapability` |
| TASK-012 | #2694 | record why setup-prometheus-auth.py has no indent bug |
| TASK-127 | #2695 | log `ABS_API_ENABLED`'s boot-time value unconditionally |

Plus **#2689 (TASK-223)** organizer `planTargetPaths` dedupe and **#2688 (TASK-222)**
params-aware `EnqueueOp` — 14 in total. Deployed to prod by the owner on 2026-08-22
via `make deploy-debug`.

## Correction: the "LegacyOpID" follow-up was mis-diagnosed

The TASK-222 agent reported, and this coordinator repeated in #2688's body and in an
earlier version of `todo.d/20260822-legacy-opid-defeats-enqueue-dedupe.md`, that the
params-aware dedupe turned a swallowed double-click into two *serialized* maintenance
runs. **That is wrong.** Both `EnqueueOp`'s dedupe block and dispatcher Gate 3
(`dispatcher.go:107`) are gated on `def.ConcurrencyKey != ""`, and all 37 maintenance
jobs use `DefaultPolicy()`, which hardcodes `ConcurrencyKey: ""`
(`internal/maintenance/job.go:131`). Neither gate has ever applied to a maintenance
job; a double-click has always started two *concurrent* runs, before and after #2688.

The `LegacyOpID`-defeats-byte-equality observation is still true — it just is not
reachable for this family yet. The real question, and the reason it now needs an owner
decision rather than a one-line patch, is whether the 37 jobs should serialize against
themselves at all. See the todo.d fragment.

**Process note:** the claim was written into a PR body and a TODO fragment before
anyone checked the gate condition it depended on. A subagent's finding is a lead, not
a result.

## Not started

~13 Haiku wave-1 briefs remain unassigned. Eligible set = `skeleton.json` filtered to
`tier=haiku`, `wave=1`, no `depends_on`, not `review_critical`, `exact_files` disjoint
from anything in flight. Known-good candidates not yet touched: TASK-007, 034, 055,
058, 059, 060, 089, 093, 097, 141, 145, 183, 204.

## Two gate holes found and fixed during wave 1

1. **Frontend briefs gated on Vitest only.** Vitest transpiles without typechecking,
   so TASK-091 shipped a `Book[]` fixture missing three required fields: `vitest`
   passed, `tsc --noEmit` failed. `WORKER-PREAMBLE.md` now requires
   `npx tsc --noEmit -p tsconfig.json` (exit 0) for any task touching `web/`,
   regardless of what the brief's own gate says.
2. **Mutation-test restore destroys unstaged work.** `git checkout <file>` restores
   from the INDEX; for an unstaged file that is HEAD, so the fix under test is
   deleted rather than restored. Commit first, then mutate. (This bit TASK-088.)

## Reliability note

~14 subagents died to `API Error: The response stopped arriving` during this wave, at
every stage from first tool call to post-gate commit. No work was lost — worktrees
survive and can be resumed from `git status`/`git diff` — but roughly half the tasks
ended up finished by the coordinator. Worker prompts now say to commit early rather
than batch to the end.
