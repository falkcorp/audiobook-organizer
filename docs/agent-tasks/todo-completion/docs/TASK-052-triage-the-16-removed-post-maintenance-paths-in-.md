<!-- file: docs/agent-tasks/todo-completion/docs/TASK-052-triage-the-16-removed-post-maintenance-paths-in-.md -->
<!-- version: 1.0.0 -->
<!-- guid: 442d5337-026c-4296-8c6a-f112fc2d92bd -->
<!-- last-edited: 2026-08-21 -->

# TASK-052 — Triage the 16 removed POST /maintenance/* paths in openapi.json — delete, or document as ops-API equivalents (TODO.md L296)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · docs subagent · **Why:** Each of the 16 needs an individual judgment call (delete vs. redocument as its registry-op equivalent) plus writing new OpenAPI entries for the ops-API dispatch pattern if that's the chosen path — more design-adjacent than part 1's pure deletion. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 296 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**The OpenAPI spec still documents 48 endpoints th" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-01.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/docs-052-triage-the-16-removed-post-maintenance-paths-in-" -b agent/docs-052-triage-the-16-removed-post-maintenance-paths-in- origin/main
cd "$REPO/.worktrees/docs-052-triage-the-16-removed-post-maintenance-paths-in-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

For each of the 16 POST /maintenance/* paths still in openapi.json (backfill-book-files, cleanup-backups, cleanup-empty-folders, cleanup-organize-mess, cleanup-series, dedup-books, enrich-book-files, fix-author-narrator-swap, fix-book-file-paths, fix-library-states, fix-read-by-narrator, fix-version-groups, generate-itl-tests, recompute-itunes-paths, refetch-missing-authors — 15 named plus /maintenance/wipe which is confirmed to still be a real POST route and should be LEFT ALONE), confirm it has no real route (per the TODO, only /maintenance/wipe survived as an actual POST), then either delete the stale entry or replace it with documentation of its registry-ops-API equivalent (e.g. POST /api/v1/operations/maintenance.dedup-books or whatever this repo's actual ops-dispatch endpoint shape is).

## Background (verify before editing)

- Find the real ops-API dispatch route shape first: `grep -rn 'operations.*POST\|ops.*router.POST' internal/server/*.go` to see the actual endpoint pattern these operations are invoked through today, so the redocumented spec entries (if that path is chosen) are accurate rather than invented.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  test -f docs/api/openapi.json && echo OK   # 1 hit — file exists at HEAD (docs/edit target)
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Confirm /maintenance/wipe is the only surviving real POST /maintenance/* route via the real router table (reuse part 1's route-dump approach) and leave its openapi.json entry untouched.
2. For each of the other 15, verify via the real route table that no route exists at that exact path.
3. For each confirmed-gone path: EITHER delete the entry from openapi.json, OR (preferred, since these represent real ongoing capability) replace it with an accurate documentation of how to invoke the same operation today via the registry ops API — confirm the real dispatch endpoint shape from the grep in Background before writing new spec entries.
4. Get owner input on which of the two (delete vs. redocument) is preferred overall, since it applies uniformly across all 15 — this is a small policy call worth surfacing rather than guessing silently.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_docs_052.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If the real ops-API dispatch endpoint is generic (e.g. a single POST /api/v1/operations/:opID for ALL registry ops, not 15 separate paths), redocumenting as 15 separate OpenAPI paths would be WRONG — in that case a single generic entry with an enum of valid opID values is the more accurate representation; confirm the real shape before choosing.

## Tests

- Same JSON-validity check as part 1.

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] Each of the 15 non-wipe maintenance paths is either removed or replaced with an accurate ops-API-equivalent doc entry, and /maintenance/wipe's entry is unchanged.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_docs_052.md`.

## Commit message

```
refactor(docs): Triage the 16 removed POST /maintenance/* paths in openapi.j (TODO L296)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

This needs a quick owner check-in on delete-vs-redocument policy before executing at scale, even though each individual path's 'is it gone' fact is independently verifiable.
