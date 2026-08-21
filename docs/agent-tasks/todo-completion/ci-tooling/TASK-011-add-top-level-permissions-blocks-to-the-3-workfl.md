<!-- file: docs/agent-tasks/todo-completion/ci-tooling/TASK-011-add-top-level-permissions-blocks-to-the-3-workfl.md -->
<!-- version: 1.0.0 -->
<!-- guid: ab131256-1f55-47c8-8a30-fde4b30df03b -->
<!-- last-edited: 2026-08-21 -->

# TASK-011 — Add top-level `permissions:` blocks to the 3 workflows flagged by actions/missing-workflow-permissions (SEC-CODEQL-BACKLOG)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · ci-tooling subagent · **Why:** Mechanical, 3 files, same fix pattern each time — add a minimal top-level permissions block scoped to what the workflow's steps actually need. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 2595 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**SEC-CODEQL-BACKLOG**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ci-tooling-011-add-top-level-permissions-blocks-to-the-3-workfl" -b agent/ci-tooling-011-add-top-level-permissions-blocks-to-the-3-workfl origin/main
cd "$REPO/.worktrees/ci-tooling-011-add-top-level-permissions-blocks-to-the-3-workfl"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

For each of the 3 workflow files, read what GITHUB_TOKEN-consuming actions the workflow's jobs actually use (checkout, gh CLI calls, PR/issue comments, etc.), and add a top-level `permissions:` block granting only those scopes (e.g. `contents: read`, `issues: write`, `pull-requests: write` as actually needed) instead of the implicit default (often broader than necessary).

## Background (verify before editing)

- This is the standard CodeQL/actions hardening rule: a workflow with no explicit permissions: block runs with the repository's default token permissions, which may be broader than the workflow needs.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rLn '^permissions:' .github/workflows/*.yml   # 3 lines: nightly-burndown.yml, hard-burndown.yml, triage-poll.yml — exactly 3 workflow files lack a top-level permissions block
  ```

### Reuse — don't invent

- Use `an existing workflow's permissions block as a template` in `.github/workflows/*.yml (any file WITH a permissions: block)` (verify: `grep -l '^permissions:' .github/workflows/*.yml | head -1`) — do NOT write a parallel helper.

## Step-by-step

1. Read .github/workflows/nightly-burndown.yml in full; list every `uses:`/`run: gh ...` step to determine what token scopes it needs.
2. Add a `permissions:` block at the top level of the workflow (alongside `on:`/`jobs:`), listing only the needed scopes explicitly (default everything else to none by omission, or add `contents: read` as a safe baseline if unsure).
3. Repeat for hard-burndown.yml and triage-poll.yml.
4. Re-run each workflow (or at minimum validate YAML syntax) to confirm the narrowed permissions don't break a step that needed a scope not initially spotted.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_ci-tooling_011.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If a workflow calls `gh pr comment` or similar, it needs `pull-requests: write`, not just `issues: write` — verify the exact gh subcommand used, since GitHub's permission model treats PR and issue comments as needing overlapping but not identical scopes.

## Tests

- N/A — CI config change; validated by the workflow actually running successfully on its next trigger (or a manual workflow_dispatch test run if available).

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] grep -rLn '^permissions:' .github/workflows/*.yml returns 0 lines (down from 3) after the change.
- [ ] The next scheduled/triggered run of each of the 3 workflows completes without a permissions-denied error.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_ci-tooling_011.md`.

## Commit message

```
feat(ci-tooling): Add top-level `permissions:` blocks to the 3 workflows flagg (SEC-CODEQL-BACKLOG)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`grep -rLn '^permissions:' .github/workflows/*.yml returns 0 lines (down from 3) after the change.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Cheapest, most mechanical part of the whole CodeQL backlog item — good haiku-tier task.
