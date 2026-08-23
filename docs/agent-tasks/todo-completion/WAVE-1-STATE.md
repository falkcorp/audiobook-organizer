<!-- file: docs/agent-tasks/todo-completion/WAVE-1-STATE.md -->
<!-- version: 1.3.0 -->
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

## Haiku tier state as of 2026-08-22 20:45 EDT — 37 of 39 merged

**This section previously said "~13 Haiku wave-1 briefs remain unassigned" and listed
TASK-007, 034, 055, 058, 059, 060, 089, 093, 097, 141, 145, 183, 204 as known-good
candidates. All thirteen were in fact already done when that was written** —
twelve merged as #2698-#2715, and TASK-059 landed without a matching branch. The list sent a later session looking for
work that did not exist; it is replaced here with a recomputed count rather than
amended, because the shape of the claim was wrong, not just its numbers.

Recomputed from `skeleton.json` filtered to `tier=haiku` (39 tasks), resolving each
id to its branch and that branch's PR state:

| State | Count | Tasks |
|---|---|---|
| Merged | 37 | everything not named below |
| Held — `review_critical` | 2 | TASK-046, TASK-086 |

TASK-046 and TASK-086 are held on the owner's instruction ("skip both for now"), not
blocked, and both were re-checked at HEAD to confirm they are still genuinely open:
`merge.AsExternalIDReassigner` is still a bare assertion (`internal/merge/service.go:34`)
and `util.NormalizeAuthor` still does only `ToLower`+`TrimSpace`
(`internal/util/normalize.go:27`) with no internal-whitespace collapse. Per
`BREAKDOWN-2026-08-21.md`, review-critical PRs stay open for the owner regardless.

### ⚠️ How this table was computed, and where that method fails

Each `tier=haiku` id was resolved to a branch matching its number, then that branch's
PR state. **That inference is sound for "merged" and unsound for "not done."** A task
whose work landed under a differently-named branch, or straight from the coordinator,
leaves no matching branch and reads as untouched.

It produced exactly that error once here. An earlier version of this table listed
TASK-059 as "never dispatched" on the strength of an absent branch. Its work had in
fact already landed: the re-audit bullet at `TODO.md:10865` was closed 2026-08-22 and
the DEP-1e spin-out exists at `todo.d/20260822-drop-deprecated-book-itunespath.md`.
Absence of a branch is not evidence of absence of work — check the artifact the task
was supposed to produce.

### TASK-059's close-out was wrong, and is being corrected

Re-verifying the nine sub-items that bullet marks resolved found two that are not:

- **DEAD-1 is not resolved.** The close-out greps three symbols
  (`legacySaveConfigToDatabase_REMOVED`, `bookTagKeyspace`,
  `bookSummarySelectColumnsQualified`) and reports 0 hits. The original DEAD-1
  evidence at `docs/archive/codebase-evaluation.md:107` names a **fourth**:
  `linkAsVersion`. It is still present at `internal/itunes/service/importer.go:1780`
  with exactly two callers, **both tests** — which is also why no linter flags it, as
  staticcheck's U1000 counts in-package test usage as usage.
- **PERF-1 was superseded, not resolved.** The finding asked to paginate unbounded
  `GetAllBooks(0,0)` calls; commit `19e129d48` deliberately moved eleven more ops *to*
  the unbounded form to stop truncation. The prescribed direction was rejected, and
  residual memory exposure is reduced rather than eliminated.

This is the failure mode that matters most in a close-out: marking an item resolved
removes it from everyone's view permanently, so a close-out asserted from a partial
grep is worse than no close-out. Corrected in the TASK-059 PR.

### Wave 2 (2026-08-22): 9 merged

TASK-015, 019, 033, 047, 123, 144, 151 (#2726-#2732), then TASK-146 (#2734) and
TASK-139 (#2737). Close-out in #2735 and #2739; it retires **six** TODO items from
seven PRs, because TASK-015's source line is the REPO-SIZE-1 stop-for-human entry,
which it does not resolve.

### What review caught that CI did not

Reviewer agents ran over every PR in wave 2 after the workers' gates passed. Four
defects survived a green gate, which is the argument for the pass existing:

1. **TASK-146 hand-edited two ABS oracle captures** (`post_login.json`,
   `post_auth_refresh.json`) from the captured 10/600000 to 15/900000. Those are
   verbatim captures stored raw on purpose; `scripts/abs_capture_fixtures.py` writes
   the captured values straight back, so the edit would have broken both conformance
   tests later with nothing in the tree to explain why. CI could not catch it — the
   gate compares against the file the change had just rewritten. Fixed by restoring
   both captures and declaring the divergence as a named `allowance`.
2. **TASK-146 fixed two of the three mismatches its source audit lists.** The third —
   the throttle counts failures, not requests — was left, along with the comment
   `docs/audits/2026-08-11-abs-coverage-gap-audit.md` explicitly calls out as false.
3. **TASK-139 shipped with zero tests** though its brief required them; the gate
   passed by running a pre-existing test over the already-existing underlying method.
4. **TASK-144 fixed the less severe half of ABS §6.3** (omit `numBooks`) and left the
   more severe half (the non-optional narrator `id`), then added a regression test
   asserting only the half that was done — pinning the incomplete shape as correct.
   Fixed in #2738.

Two general lessons, both already paid for twice:

- **An empty golden array pins nothing about its elements.** The search oracle records
  `narrators: []`, so conformance passed vacuously over a missing required field. The
  target-client contract records this same failure mode from an earlier occurrence.
- **A per-endpoint shape test cannot see two endpoints disagreeing.** #2738's first
  attempt gave the narrator id the right format over the wrong name source, so it
  decoded cleanly and resolved to nothing. The shape test passed on that build; only
  a cross-endpoint assertion caught it.

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
