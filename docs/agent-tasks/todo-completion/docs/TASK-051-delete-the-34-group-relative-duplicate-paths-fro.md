<!-- file: docs/agent-tasks/todo-completion/docs/TASK-051-delete-the-34-group-relative-duplicate-paths-fro.md -->
<!-- version: 1.0.0 -->
<!-- guid: a244eedf-9bff-4633-9ff2-98ca749867b8 -->
<!-- last-edited: 2026-08-21 -->

# TASK-051 — Delete the 34 group-relative duplicate paths from docs/api/openapi.json (safe to delete on sight) (TODO.md L296)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · docs subagent · **Why:** Mechanical JSON-path deletion, but requires care not to delete a path that ALSO happens to be a legitimately root-level route (verify each against the real router table before deleting, per the TODO's own caution that this needs 'individual confirmation'). · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 296 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**The OpenAPI spec still documents 48 endpoints th" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-01.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/docs-051-delete-the-34-group-relative-duplicate-paths-fro" -b agent/docs-051-delete-the-34-group-relative-duplicate-paths-fro origin/main
cd "$REPO/.worktrees/docs-051-delete-the-34-group-relative-duplicate-paths-fro"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Remove the group-relative duplicate path entries from docs/api/openapi.json's paths object — the ones whose correctly-prefixed twin (e.g. /auth/login for /login) already exists in the spec — after confirming each against the real router table via s.router.Routes(), not by grepping code.

## Background (verify before editing)

- The TODO's full list (37 group-relative + torrents + maintenance entries, minus /path and /compare already removed) spans: DELETE /invites/{token}, DELETE /sessions/{id}, DELETE /{id}, GET /books, GET /import-status/{id}, GET /invites, GET /library-status, GET /me, GET /sessions, GET /status, GET /{id}, GET /{id}/results, POST /accept-invite, POST /import, POST /import-status/bulk, POST /invite, POST /login, POST /logout, POST /rebuild, POST /setup, POST /sync, POST /test-connection, POST /test-mapping, POST /validate, POST /write-back, POST /write-back-all, POST /write-back/preview, POST /{id}/apply, POST /{id}/cancel, POST /{id}/deactivate, POST /{id}/reactivate, POST /{id}/reset-password.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  python3 -c "import json; d=json.load(open('docs/api/openapi.json')); print(sum(1 for p in ['/login','/logout','/books','/me','/sessions','/status','/invites'] if p in d['paths']))"   # 7 (all sampled group-relative duplicates still present) — 34 of 36 sampled stale paths are still present in the live spec
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Write a small throwaway Go test (or reuse an existing one, e.g. under internal/server) that calls s.router.Routes() on a fully-wired test server and dumps the real route table to a file for comparison — do not trust a static code grep for this, per the TODO's own stated methodology.
2. For each path in the list above, confirm the REAL route table has a correctly-prefixed twin (e.g. /auth/login for /login) and that the bare group-relative form is NOT itself a registered route.
3. Delete each confirmed-duplicate path (and its method entries) from docs/api/openapi.json's `paths` object, preserving valid JSON structure.
4. If a docs/api generation/lint script exists (`grep -rln openapi.json Makefile .github/workflows/`), re-run it to confirm the edited spec still validates.

Then, always:
- Keep the change purely removal — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_docs_051.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A route registered through BOTH a direct top-level path AND inside a group (unlikely but possible for back-compat) must not be deleted — this is exactly why step 2 requires checking the REAL route table, not assuming from the list alone.

## Tests

- If an OpenAPI schema-validation CI step exists, re-running it locally is the acceptance check; otherwise validate with a JSON schema linter (`python3 -c "import json; json.load(open('docs/api/openapi.json'))"`, or a proper OpenAPI validator if the repo has one wired in).

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] The 34 confirmed-duplicate paths are absent from docs/api/openapi.json's paths object, and `python3 -c "import json; json.load(open('docs/api/openapi.json'))"` still parses without error.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_docs_051.md`.

## Commit message

```
refactor(docs): Delete the 34 group-relative duplicate paths from docs/api/o (TODO L296)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the removed symbol/file is already ABSENT at HEAD (re-run the re-verify greps above: zero hits = already removed) AND the replacement is present, the removal is already done — run acceptance instead. Rollback = `git revert` the commit to restore the file + its call sites; no data or schema is touched.

## Coordinator notes

Split from the same L296 TODO item; part 2 covers the 16 removed-maintenance-endpoint paths (need individual triage, not blind deletion) and part 3 covers /torrents.
