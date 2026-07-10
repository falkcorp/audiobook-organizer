<!-- file: docs/agent-tasks/bug-techdebt/TASK-06-repo-size-rewrite-plan.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7846b1d6-fda7-4b12-9a49-598fa69b9a7d -->
<!-- last-edited: 2026-07-10 -->

# TASK-06 — REPO-SIZE-1: produce the history-rewrite migration plan, then STOP (REPO-SIZE-1, #1650) [⛔ STOP-FOR-HUMAN]

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · research-and-plan-writing subagent · **Why:** read-only auditing + a decision-quality document; zero code, but the analysis must be evidence-grounded · **Depends on:** none

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY per item (worktree/PR/CI) EXCEPT REPO-SIZE-1 (#1650) which is STOP-FOR-HUMAN: a git-history rewrite is destructive and invalidates every clone/worktree — produce the migration plan (BFG/filter-repo vs LFS options, coordination checklist, backup strategy) as a TASK brief whose ONLY deliverable is the plan document, then STOP.
**File-ownership:** none — this task creates one new docs file and touches no code. ⛔ THE ONLY DELIVERABLE IS THE PLAN DOCUMENT. Executing ANY history-mutating command (`git filter-repo`, `bfg`, `git filter-branch`, `git push --force*`, `git lfs migrate`) under this task is a critical violation — the gate above says STOP-FOR-HUMAN.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/bug-techdebt-repo-size-rewrite-plan" -b agent/bug-techdebt-repo-size-rewrite-plan origin/main
cd "$REPO/.worktrees/bug-techdebt-repo-size-rewrite-plan"
git rebase origin/main
```

## Goal

Write `docs/plans/2026-07-10-repo-size-history-rewrite-plan.md` — a complete,
human-decidable migration plan for shrinking the repo (GitHub reports 1.69 GB;
issue #1650). Audit first (read-only), then present options with a recommendation.
Then **STOP: open the PR for the document itself, and end with an explicit
"STOP-FOR-HUMAN: awaiting owner decision" report. Do not schedule, prepare, or
execute any rewrite.**

## Background (verify before editing)

- Planning-time measurement discrepancy you must resolve in the audit: GitHub-side
  size 1.69 GB (issue #1650) vs local `git count-objects -vH` → `size-pack: 228.41 MiB`
  (2026-07-10). Candidate explanations to check: GitHub counts all refs incl. PR heads
  (`refs/pull/*`), unreachable objects awaiting server-side gc, and Actions caches are
  NOT part of repo size — attribute the delta with evidence, don't guess.
- Issue #1650 already sketches the approach (blob audit command, external fixture
  hosting, `git filter-repo` over `git filter-branch`, coordinate before force-push,
  .gitignore + push protection afterwards). Your plan supersedes the sketch with
  concrete numbers and a decision matrix.
- Repo-specific coordination surface the checklist MUST cover: active worktrees under
  `.worktrees/` (this repo's standard flow — ALL become invalid), other live Claude
  sessions' worktrees, open PRs (rebase impossibility after rewrite), the `.standards/`
  submodule pointer (unaffected but verify), CI (SHA-pinned actions unaffected;
  branch-protection `required_linear_history` interaction with a force-push), release
  tags (GoReleaser reads tags — a rewrite that touches tagged history breaks
  `GORELEASER_CURRENT_TAG` assumptions), and the `jdfalk-ci-bot` GitHub App used for
  tag pushes.

- **Re-verify these anchors before writing** — line numbers drift:
  ```bash
  gh issue view 1650 --repo falkcorp/audiobook-organizer --json state -q .state
  # Expected: OPEN
  git count-objects -vH | grep size-pack
  # Expected: a number in the hundreds of MiB locally (228.41 MiB at planning time)
  ```

## Step-by-step

1. Run the blob audit (read-only; from the issue, verbatim):
   ```bash
   git rev-list --objects --all | git cat-file --batch-check='%(objecttype) %(objectname) %(objectsize) %(rest)' | sort -k3 -n -r | head -40
   ```
   Expected: a ranked list; record the top-40 blobs with paths, sizes, and whether
   each path still exists at HEAD (`git cat-file -e HEAD:<path> && echo live || echo historical`).
2. Aggregate by path/extension to name the offender classes (expected per #1650: test
   media fixtures). Compute: total bytes in blobs >1 MiB; % of pack they represent;
   the pack size a rewrite would plausibly reach.
3. Resolve the 1.69 GB vs 228 MiB discrepancy (see Background). If GitHub-side bloat
   is mostly unreachable/PR-ref objects, a support-ticket gc may recover most of it
   WITHOUT any history rewrite — if the audit supports that, say so prominently: it
   changes the recommendation.
4. Write `docs/plans/2026-07-10-repo-size-history-rewrite-plan.md` (repo-standard
   4-line header, fresh uuid4 via `uuidgen | tr 'A-Z' 'a-z'`) with sections:
   - **Audit results** (step 1-3 evidence, tables).
   - **Options compared**: (a) `git filter-repo` rewrite (NOT `git filter-branch`),
     (b) BFG Repo-Cleaner, (c) `git lfs migrate` (history-rewriting LFS adoption),
     (d) forward-only: stop committing fixtures + external fixture host
     (`testdata/fetch.go` downloader per #1650) + GitHub support gc, NO rewrite.
     For each: expected size, tooling risk, tag/release impact, GitHub-side steps.
   - **Recommendation** with reasoning.
   - **Coordination checklist** (every item from Background, each with a concrete
     verification command, ordered: freeze merges → inventory worktrees/PRs → backup
     → rewrite → force-push → re-clone protocol for every consumer → un-freeze).
   - **Backup strategy**: `git clone --mirror` to two locations (the server
     172.16.2.30 + a second copy), verification commands, retention until sign-off.
   - **Rollback**: mirrors are authoritative; push-back procedure; GitHub support
     contact path if the rewrite must be undone after gc.
   - **Explicit final line**: `STATUS: STOP-FOR-HUMAN — no rewrite executed; awaiting owner decision.`
5. Prepend a CHANGELOG.md entry; add a "plan written, awaiting decision" note to the
   TODO.md REPO-SIZE-1 item (locate: `grep -n 'REPO-SIZE-1' TODO.md` — Expected: ≥1
   hit). Do NOT check the item off — it is not done until the human decides + executes.
6. Bump headers on touched files; run the gate; open the PR (the plan document IS the
   PR); merge it; then STOP and report `STOP-FOR-HUMAN` with the audit's headline numbers.

Anti-over-suppression: N/A (docs-only task; no filter/guard/veto/skip/dedupe path).

## How to test

```bash
test -f docs/plans/2026-07-10-repo-size-history-rewrite-plan.md && head -4 docs/plans/2026-07-10-repo-size-history-rewrite-plan.md
# Expected: file exists; 4-line header with today's date
grep -n 'STOP-FOR-HUMAN' docs/plans/2026-07-10-repo-size-history-rewrite-plan.md
# Expected: ≥1 hit including the STATUS final line
make ci
```

staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files
you changed; the merge gate is Minimal CI green. The `sdkguard` step is ALSO red on
main (#1795, fixed by TASK-03) — pre-existing, not yours. (Docs-only diff: `make ci`
should behave exactly as on main.)

## Acceptance criteria

- [ ] `git diff --stat origin/main` shows ONLY: the new plan doc, CHANGELOG.md, TODO.md (no code, no workflow, no git config)
- [ ] `git reflog` and `git log --oneline -3` show NO rewrite/force operations were performed (history untouched)
- [ ] Plan doc contains all 7 sections from step 4 and the literal final `STATUS: STOP-FOR-HUMAN` line
- [ ] The 1.69 GB vs local-pack discrepancy is explained with evidence, not hand-waved
- [ ] Anti-over-suppression: N/A
- [ ] Tests green (nothing to run beyond `make ci` on a docs diff); file headers present on the new doc and bumped on TODO/CHANGELOG
- [ ] Final report says `STOP-FOR-HUMAN` with COMPLETED/REMAINING/BLOCKED counts

## Commit message

```
docs(plans): REPO-SIZE-1 history-rewrite migration plan — STOP-FOR-HUMAN (#1650)

Blob audit + options matrix (filter-repo vs BFG vs LFS-migrate vs forward-only
+ support gc), coordination checklist, dual-mirror backup strategy. No rewrite
executed; awaiting owner decision per the INIT-9 gate.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/bug-techdebt-repo-size-rewrite-plan
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `test -f docs/plans/2026-07-10-repo-size-history-rewrite-plan.md` succeeds (on
origin/main), the plan is already written — verify the acceptance checks instead of
re-writing, and re-issue the STOP-FOR-HUMAN report. Rollback = revert the single
commit (deletes the document); the repository's history and size are untouched by this
task in every branch of every outcome.
