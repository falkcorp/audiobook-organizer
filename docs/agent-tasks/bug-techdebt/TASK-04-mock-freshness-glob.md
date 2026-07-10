<!-- file: docs/agent-tasks/bug-techdebt/TASK-04-mock-freshness-glob.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9e6aa9f0-1e51-4e35-ac79-38299346c70b -->
<!-- last-edited: 2026-07-10 -->

# TASK-04 — Fix the Mock-Freshness CI glob to cover nested mocks dirs (MOCK-FRESHNESS-GLOB-GAP, #1797)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Haiku-class · one-file CI-config edit subagent · **Why:** two-line mechanical pathspec swap with a fully-scripted local verification · **Depends on:** none

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY per item (worktree/PR/CI) EXCEPT REPO-SIZE-1 (#1650) which is STOP-FOR-HUMAN: a git-history rewrite is destructive and invalidates every clone/worktree — produce the migration plan (BFG/filter-repo vs LFS options, coordination checklist, backup strategy) as a TASK brief whose ONLY deliverable is the plan document, then STOP.
**File-ownership:** none — no other task touches `.github/workflows/ci.yml`. If step 5 finds already-stale nested mocks, the regenerated `internal/**/mocks/**` files also land in this PR — no other INIT-9 task touches any mocks dir, so this adds no collision. WORKFLOW-FILE RULE: push via plain `git push` only, NEVER via the MCP GitHub contents API (it has corrupted workflow files before).

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/bug-techdebt-mock-freshness-glob" -b agent/bug-techdebt-mock-freshness-glob origin/main
cd "$REPO/.worktrees/bug-techdebt-mock-freshness-glob"
git rebase origin/main
```

## Goal

Make the "Check mockery mocks are fresh" step in `.github/workflows/ci.yml` see ALL
`mocks/` directories, including the 8 nested `internal/server/handlers/*/mocks/` dirs
it misses today. Root cause: the pathspec `internal/*/mocks/` is UNQUOTED, so the
runner's shell expands it (shell `*` never crosses `/`) to only the 6 one-level-deep
dirs before `git diff` ever sees a pattern. Fix: replace it with the QUOTED git
pathspec `':(glob)internal/**/mocks/**'` on both lines that use it.

## Background (verify before editing)

- Verified at planning time: `git ls-files ':(glob)internal/**/mocks/*'` matches nested
  dirs (e.g. `internal/server/handlers/dedup/mocks/`), while unquoted shell expansion
  of `internal/*/mocks/` yields only `internal/{ai,database,logger,metadata,operations,scanner}/mocks/`.
- The gap let `mock_dedup_engine.go` go stale from #1736 until hand-regenerated in #1757.
- The two other pathspecs on the same lines (`internal/ai/mock_*_test.go`,
  `internal/metadata/mock_*_test.go`) are correct and STAY unchanged.
- The `2>/dev/null` suffix stays (it guards the no-such-path case).

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'internal/\*/mocks/' .github/workflows/ci.yml
  # Expected: exactly 2 hits (~:89 the `git diff --quiet`, ~:91 the `git diff --stat`)
  find internal -type d -name mocks | wc -l
  # Expected: ~14 dirs total (6 one-level + 8 nested under internal/server/handlers/)
  ```

## Step-by-step

1. Open `.github/workflows/ci.yml`, locate both hits from the grep above.
2. On BOTH lines, replace the token `internal/*/mocks/` with `':(glob)internal/**/mocks/**'`
   (single-quoted, exactly as written — the quotes are the fix: they stop the shell
   from expanding it and hand the pattern to git's pathspec engine). Leave the two
   `mock_*_test.go` pathspecs and everything else on those lines untouched.
3. Verify locally that the new pattern catches a NESTED stale mock. Run:
   ```bash
   NESTED=$(find internal/server/handlers -type d -name mocks | head -1)/$(ls $(find internal/server/handlers -type d -name mocks | head -1) | head -1)
   echo "// task-04 probe" >> "$NESTED"
   git diff --quiet -- ':(glob)internal/**/mocks/**' internal/ai/mock_*_test.go internal/metadata/mock_*_test.go 2>/dev/null; echo "exit=$?"
   ```
   Expected: `exit=1` (dirty detected — the OLD pattern would have printed `exit=0`
   for this nested path). Then revert the probe. Run: `git checkout -- "$NESTED"; git status --porcelain`
   Expected: empty output.
4. Also confirm the pattern still catches a TOP-LEVEL mock (repeat step 3 against a
   file in `internal/database/mocks/`). Expected: `exit=1`, then revert.
5. **MANDATORY pre-merge: verify the newly-covered nested mocks are CURRENTLY fresh
   on main** (steps 3-4 only prove detection works — they say nothing about whether a
   nested mock is ALREADY stale, the exact bug class that motivated this fix,
   #1736→#1757). Run:
   ```bash
   go install github.com/vektra/mockery/v3@v3.7.1   # MUST be exactly the CI-pinned version (ci.yml pins v3.7.1); any other version regenerates unrelated mocks repo-wide and fakes staleness
   mockery
   git status --porcelain -- ':(glob)internal/**/mocks/**' internal/ai/mock_*_test.go internal/metadata/mock_*_test.go
   ```
   Expected: empty output (all mocks, including the 8 nested
   `internal/server/handlers/*/mocks/` dirs, are fresh). If ANY file shows a diff, it
   is already stale on main: COMMIT the regenerated mock(s) in THIS same PR so the
   tightened gate lands green-covering-green — merging the glob fix over a stale
   nested mock would turn Minimal CI red on origin/main and halt every other wave-1
   merge under the coordinator protocol. Paste the (empty or regenerated-and-fixed)
   output in the PR body.
6. Bump the workflow file's version header (version + last-edited); keep its guid.
7. Update CHANGELOG.md (prepend) and check off the TODO.md item (locate:
   `grep -n 'MOCK-FRESHNESS-GLOB-GAP' TODO.md` — Expected: 1 hit ~:1288).
8. Run the gate (below).

Anti-over-suppression: N/A in the code sense — but step 4 is the equivalent proof in
the CI-gate direction: the widened pattern must still catch everything the old one did.

## How to test

```bash
python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/ci.yml')); print('yaml ok')"
# Expected: "yaml ok" (quoting error would break the workflow file)
make ci
```

staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files
you changed; the merge gate is Minimal CI green. The `sdkguard` step is ALSO red on
main (#1795, fixed by TASK-03) — a failure listing only `internal/logger` +
`internal/dedup/unified` is pre-existing, not yours. After merging, confirm the
"Check mockery mocks are fresh" step passes on the PR's own Minimal CI run — that IS
the end-to-end test of this change.

## Acceptance criteria

- [ ] `grep -n 'internal/\*/mocks/' .github/workflows/ci.yml` returns 0 hits (old shell-glob gone)
- [ ] `grep -c ":(glob)internal/\*\*/mocks/\*\*" .github/workflows/ci.yml` returns 2 (both lines converted)
- [ ] Step-3 nested probe returned `exit=1` and step-4 top-level probe returned `exit=1` (paste both outputs in the PR body)
- [ ] Step-5 pre-merge freshness check ran with mockery v3.7.1 and its `git status --porcelain` output is empty — either clean from the start, or any already-stale nested mocks were regenerated and committed IN THIS PR (output pasted in the PR body)
- [ ] Minimal CI green on the PR, including the Mock Freshness step
- [ ] Anti-over-suppression: N/A (step 4 is the no-narrowing proof)
- [ ] File headers bumped on every changed file

## Commit message

```
ci(mocks): quote mock-freshness pathspec so nested mocks dirs are checked (#1797)

internal/*/mocks/ was unquoted, so the runner shell expanded it one level deep
and the 8 internal/server/handlers/*/mocks/ dirs were invisible to the gate
(stale mock_dedup_engine.go escape, #1736→#1757). ':(glob)internal/**/mocks/**'
hands the recursive pattern to git's pathspec engine instead.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/bug-techdebt-mock-freshness-glob
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "(glob)internal/\*\*/mocks" .github/workflows/ci.yml` hits AND
`grep -n 'internal/\*/mocks/' .github/workflows/ci.yml` returns 0 hits, the transform
is already done — run the acceptance checks instead of re-applying. Rollback = revert
the single commit; the gate returns to the narrower (gappy) coverage, nothing else is
affected — this file only tightens CI, it cannot break builds.
