<!-- file: docs/agent-tasks/todo-completion/ci-tooling/TASK-191-bump-the-github-common-reusable-workflow-pins-in.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9266b877-6921-4429-8bdc-53fc62f0db2e -->
<!-- last-edited: 2026-08-21 -->

# TASK-191 — Bump the github-common reusable-workflow pins in at least two PRs, low-consequence first (TODO.md L921)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · ci-tooling subagent · **Why:** mechanical version-pin bumps across 8 files but requires splitting into >=2 sequenced PRs with a nightly-run wait between the low- and high-consequence groups, which is process/sequencing work, not pure mechanics · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 921 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**at least two PRs**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-02.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ci-tooling-191-bump-the-github-common-reusable-workflow-pins-in" -b agent/ci-tooling-191-bump-the-github-common-reusable-workflow-pins-in origin/main
cd "$REPO/.worktrees/ci-tooling-191-bump-the-github-common-reusable-workflow-pins-in"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Bump all 7 stale falkcorp/github-common SHA pins to the current main SHA, split across at least 2 PRs ordered by consequence: PR 1 bumps triage-poll.yml, hard-burndown.yml, nightly-burndown.yml (low-consequence, run nightly/on-demand) and is left to run at least once before PR 2 bumps frontend-ci.yml, nightly.yml, security.yml, release-prod.yml, prerelease.yml (release and security paths).

## Background (verify before editing)

- TODO.md L921-L327: 'release-prod.yml and prerelease.yml are the risk: a reusable release workflow that broke somewhere in those 22 commits is not discovered until someone cuts a release... Bump the low-consequence ones (triage-poll, the burndowns) first and let them run a nightly before touching release or security.'

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rn 'd0c3326b96557c8ea9117c1c196b628e5e028186' .github/workflows/*.yml   # 7 hits across hard-burndown.yml, nightly-burndown.yml, triage-poll.yml, frontend-ci.yml, nightly.yml, security.yml, release-prod.yml, prerelease.yml — 7 workflow files pin the same 2026-07-05 SHA
  grep -n '828afb50d0d18c426c72a6ed1060123677bee674' .github/workflows/ci.yml   # 1 hit L45 — ci.yml pins a different, newer SHA (2026-08-18)
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Resolve the current HEAD SHA of falkcorp/github-common's main branch (or the specific commit the owner wants pinned) -- this requires a live lookup (gh api or git ls-remote), not something derivable from this repo alone.
2. PR 1: update the `uses:` line's SHA (and its trailing `# main <date> (...)` comment, matching the existing comment convention) in .github/workflows/triage-poll.yml, hard-burndown.yml, nightly-burndown.yml only.
3. Merge PR 1, wait for at least one scheduled/nightly run of each of those three workflows to go green.
4. PR 2: update the same SHA in .github/workflows/frontend-ci.yml, nightly.yml, security.yml, release-prod.yml, prerelease.yml.
5. Do NOT touch .github/workflows/ci.yml's reusable-ci-minimal.yml pin (828afb50) -- it is already current; this item is only about the 7 stale pins converging to the same commit ci.yml already uses (or a newer one, if the owner wants to advance both together).

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_ci-tooling_191.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A workflow file bumped in PR 1 that has no near-term scheduled trigger (verify each of the three actually has a `schedule:` or is easy to workflow_dispatch) -- if any lacks one, trigger it manually to get the 'let it run once' signal before PR 2.

## Tests

- n/a -- CI workflow files are validated by GitHub Actions itself running the updated workflows; no local unit test applies.

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] After PR 1 merges: the next scheduled run of triage-poll.yml, hard-burndown.yml, and nightly-burndown.yml (or a manual workflow_dispatch trigger) completes with a green status.
- [ ] After PR 2 merges: grep -rn 'd0c3326b96557c8ea9117c1c196b628e5e028186' .github/workflows/*.yml returns 0 hits (all pins converged to the new SHA).
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_ci-tooling_191.md`.

## Commit message

```
refactor(ci-tooling): Bump the github-common reusable-workflow pins in at least tw (TODO L921)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

This is process/sequencing work as much as a code change -- flag to the coordinator that PR 1 and PR 2 must NOT be dispatched in parallel (they are explicitly sequenced with a real-world wait between them), unlike most items in this scope.
