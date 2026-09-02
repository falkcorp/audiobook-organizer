<!-- file: docs/agent-tasks/todo-completion/docs/TASK-055-document-the-todo-d-fragment-race-assembled-betw.md -->
<!-- version: 1.1.0 -->
<!-- guid: cc064b9b-07f6-43b8-bf53-4e4219ec173d -->
<!-- last-edited: 2026-09-02 -->

# TASK-055 — Document the todo.d fragment race (assembled between filing and finishing) as a process rule, not a mechanical guard (TODO.md L1852)

> **Status 2026-09-02:** ✅ DONE — PR #2714 merged 2026-08-22 (d7f665de1).

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · docs subagent · **Why:** Pure documentation addition with the exact wording/placement already specified by the item text. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 1852 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**A `todo.d` fragment assembled between the PR tha" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/docs-055-document-the-todo-d-fragment-race-assembled-betw" -b agent/docs-055-document-the-todo-d-fragment-race-assembled-betw origin/main
cd "$REPO/.worktrees/docs-055-document-the-todo-d-fragment-race-assembled-betw"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a rule to todo.d/README.md and to CLAUDE.md's 'Post-Task Hygiene' section: when a PR completes work that had a todo.d fragment, grep TODO.md for the corresponding entry before merging and check it off if assemble_todo.py already folded it in and deleted the fragment — since a deleted fragment in a finishing PR's diff proves nothing (assemble_todo.py always deletes fragments on fold-in, whether or not a later PR also 'deletes' an already-gone file).

## Background (verify before editing)

- scripts/assemble_todo.py's main() calls git_rm(fragments) at the end, deleting each fragment as it folds it into TODO.md — so by the time a finishing PR merges, the fragment may already be gone from main, making that PR's own deletion of it a silent no-op.
- The item explicitly rules out two mechanical-guard shapes as dead ends: (1) 'if a PR deletes a todo.d fragment, require the matching TODO.md entry to be checked off' fails because after a rebase the deletion may not appear in the PR's diff at all; (2) 'flag any unchecked TODO.md entry whose fragment marker points at a missing fragment' matches EVERY assembled entry since assemble always deletes, giving false positives.
- A softer mechanical option the item allows as acceptable-if-wanted: a heuristic PR-body check requiring PRs that say 'closes the todo.d fragment...' or delete a fragment to also touch TODO.md — explicitly framed as a heuristic, not a guarantee, and not the primary ask.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  test -f todo.d/README.md && echo exists   # exists — todo.d/README.md exists as the target for the new rule
  grep -n 'Post-Task Hygiene' CLAUDE.md   # 1 hit, the existing section header — CLAUDE.md already has the CHANGELOG/TODO/executive-summary post-task hygiene triple this item wants extended
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In todo.d/README.md, add a short subsection (e.g. under an existing 'gotchas'/'notes' heading, or a new one) stating: a fragment can be assembled into TODO.md and deleted by scripts/assemble_todo.py's periodic run BEFORE the PR that actually finishes the described work merges — so when your PR completes work that had a todo.d fragment, grep TODO.md for the fragment's content/title before merging and manually check off the entry if it's already there, rather than assuming your PR's own fragment deletion (if the fragment file is even still present) will do it.
2. In CLAUDE.md, extend the existing 'Post-Task Hygiene' section (which already lists CHANGELOG/TODO/executive-summary) to mention this specific case: finishing a task that had a todo.d fragment requires checking TODO.md for an already-assembled entry describing the same work, not just adding a new fragment for anything left over.
3. Optionally (item marks this as 'if a mechanical guard is wanted anyway', not required): note the PR-body heuristic as a possible future CI check, explicitly flagged as a heuristic rather than a guarantee, so a future contributor doesn't try to harden it into something it structurally cannot be.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_docs_055.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- (none)

## Tests

- (none)

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] Both todo.d/README.md and CLAUDE.md contain the new rule text; `grep -n 'assembled between' todo.d/README.md CLAUDE.md` (or similar phrase used) returns hits in both files.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_docs_055.md`.

## Commit message

```
feat(docs): Document the todo.d fragment race (assembled between filing  (TODO L1852)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `Both todo.d/README.md and CLAUDE.md contain the new rule text; `grep -n 'assembled between' todo.d/README.md CLAUDE.md` (or similar phrase used) returns hits in both files.` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Real 2026-08-10 incident: PR #2272 (04:25 EDT) added a fragment, assemble_todo.py folded+deleted it at 04:51 EDT (26 minutes later), PR #2273 (05:12 EDT) did the actual work and 'deleted' the already-gone fragment — leaving a stale unchecked TODO.md entry, cleaned up by hand in #2274.
