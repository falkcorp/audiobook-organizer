<!-- file: docs/agent-tasks/todo-completion/docs/TASK-053-delete-the-torrents-group-relative-fragment-from.md -->
<!-- version: 1.1.0 -->
<!-- guid: 53647641-55f9-40a4-937a-505a549fa973 -->
<!-- last-edited: 2026-09-02 -->

# TASK-053 — Delete the /torrents group-relative fragment from openapi.json (TODO.md L296)

> **Status 2026-09-02:** ✅ DONE — PR #2686 merged 2026-08-22 (153a88f1c).

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · docs subagent · **Why:** Single-path deletion, same mechanical pattern as part 1. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 296 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**The OpenAPI spec still documents 48 endpoints th" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-01.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/docs-053-delete-the-torrents-group-relative-fragment-from" -b agent/docs-053-delete-the-torrents-group-relative-fragment-from origin/main
cd "$REPO/.worktrees/docs-053-delete-the-torrents-group-relative-fragment-from"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Confirm GET /torrents has no direct registered route (only its prefixed Deluge-group twin does) via the real route table, then delete the bare entry from docs/api/openapi.json.

## Background (verify before editing)

- Could be folded into part 1's batch execution since it's the identical bug class — kept as a separate JSON object here only because the source TODO called it out as its own numbered group (group 3 of 3).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  test -f docs/api/openapi.json && echo OK   # 1 hit — file exists at HEAD (docs/edit target)
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Confirm via the real route table (reuse part 1's dump) that /torrents has no direct route, only a Deluge-group-prefixed twin.
2. Delete the /torrents entry from docs/api/openapi.json.

Then, always:
- Keep the change purely removal — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_docs_053.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- N/A

## Tests

- Same JSON-validity check as part 1.

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] /torrents is absent from docs/api/openapi.json's paths object.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_docs_053.md`.

## Commit message

```
refactor(docs): Delete the /torrents group-relative fragment from openapi.js (TODO L296)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the removed symbol/file is already ABSENT at HEAD (re-run the re-verify greps above: zero hits = already removed) AND the replacement is present, the removal is already done — run acceptance instead. Rollback = `git revert` the commit to restore the file + its call sites; no data or schema is touched.

## Coordinator notes

Trivially batchable with part 1's execution — separated here only to mirror the source TODO's own grouping.
