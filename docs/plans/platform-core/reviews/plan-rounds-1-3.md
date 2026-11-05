<!-- file: docs/plans/platform-core/reviews/plan-rounds-1-3.md -->
<!-- version: 1.1.0 -->
<!-- guid: 0318e3b5-801f-49fc-834f-d3365cb54d8f -->
<!-- last-edited: 2026-11-05 -->

# Plan review, rounds 1–3, and the advisor review

Scope: [`implementation-plan.md`](../implementation-plan.md), the briefs under
[`docs/agent-tasks/platform-core/`](../../../agent-tasks/platform-core/README.md), and the inventory they are
generated from, checked against the spec under `docs/specs/platform-core/`. Every finding was dispositioned and
applied before the next round. Each round re-ran the mechanical wave checks (schema, dependencies in earlier
waves, file-set disjointness inside each wave, 04 coverage, gate ownership, real paths), which pass.

| Round | Lenses | BLOCKER | MAJOR | MINOR |
|---|---|---|---|---|
| 1 | order and gating, spec coverage, data safety, cold executability | 4 | 17 | 12 |
| 2 | verification of round 1, fresh adversarial pass, mechanical audit | 1 | 12 | 12 |
| 3 | verification of round 2, sweep of the least-reviewed rows | 0 | 8 | 10 |

## Round 1

- **BLOCKERs.**
  - The release freeze was switched on inside the F3 flip PR, so a release run already in flight could publish a
    half-flipped `main`. The fix: new rows R1-35 (the freeze toggle) and F3-08 (the R2 freeze-on), each merged only
    when no release run is in flight; F3-07 only embeds the recorded pre-freeze tag.
  - No row built the revocation journal's integrity or its boot re-apply, so a restore could bring back revoked
    keys. The fix: new row F4-11.
  - The R2 test corpus would have quarantined a corrupt API key, against D13. The fix: identity blockers move to
    the blocker corpus.
  - No row built core's logging pipeline. The fix: new row R1-36.
- **MAJORs** included:
  - the coretest harness (new row R1-37);
  - the factory-reset, kernel, HTTP and metrics test tables, each test given one owner;
  - core's half of the M7 level step;
  - R2 cut from the deployed RC tag;
  - one mechanism for how each flip numbers its level step and M8.

## Round 2

- **BLOCKER.** R1-36's caller rewrite would have tripped the direct-`slog` guard, whose ratchet only goes down.
  The fix: callers move onto books-side helpers in `internal/logger/`.
- **MAJORs** included:
  - the freeze rows exclude draft releases, share one pre-freeze tag reader and wait out in-flight release runs;
  - F4-11 builds the rbac and audit restore hooks, and F4-08 registers the journal's health tokens;
  - the step constructors take their level from the flip that registers them;
  - the remaining metric scopes get series goldens.
- **The level-step correction.** The spec numbers versioned steps contiguously per owner. So a flip adds a new
  level step and keeps the earlier ones, while M8, which is named by the step it follows, is replaced.

## Round 3

- **MAJORs** included:
  - the R2 corpus's store files would have been dropped by `.gitignore` (fixed with a negation block and a
    hashed manifest);
  - three spec-named archtests had no file;
  - route rows before the identity flip tested permissions that stage 13 does not yet enforce (those cells are
    skipped until F4-08 and un-skipped by the flips that mount the routes);
  - the library transfer-recovery task had no schedule key (spec gap G22, fixed in the spec).

## Advisor review

An independent advisor read the plan, the index and 14 briefs (the critical-path T4 rows among them). The verdict:
**READY WITH CHANGES**. The changes accepted and applied:

- **Schedule.** Each T4 row and flip carries half a day of independent review, and the three largest flips
  (F3-07, F4-08, F5-18) are priced at 6 days. The base case is called a floor; the advisor expects 95–120
  working days.
- **Independent review** before every T4 or flip PR merges.
- **Production-shaped store.** The cutover is also rehearsed on a store the pre-freeze binary has written, and
  the owner's dry run on a copy of production is mandatory.
- **Hotfix procedure.** A procedure for a production hotfix during the R2 freeze.
- **Committed tools.** The inventory and the wave checker are committed under `tools/`, and a task that widens
  its file list re-runs the checker first.
- **Base and entry criterion.** The plan's base is a real `main` commit, and W01 starts only after the spec PR
  merges.
- **Shell in R3.** The shell (F9) ships in R3 by default, taking a URL-changing flip out of the one-way release.
- **Tiers.** Four rows move up a tier to match their own risk notes (R0-05, R0-03, R1-27) or a checker's
  surprises (R0-01).
- **Execution rules** that reconcile the briefs with the repository's own rules: the brief is the approved plan,
  executive summaries and TODO check-offs go in the release-closing PRs, coined names go in `seams.d/`, and
  multi-session work keeps a checklist of steps.

Deferred to the owner:
- dropping the per-flip upgrade test, which needs a spec amendment;
- the release freeze as a repository variable.

Kept: R1-06 (09 places it in R1).

## Result

- **Size.** 134 tasks: 128 agent tasks and 6 owner checkpoints, in 46 waves.
- **Schedule.** The critical path is 82 working days at base rates, or 106.5 if every wave waits for the one
  before; the advisor's estimate is 95–120.
- **Spec fixes.** The rounds added five spec gap fixes: G18–G21 from the brief sync and G22 from round 3.
