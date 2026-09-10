<!-- file: docs/agent-tasks/todo-completion-2026-09/web/TASK-304-author-merge-preview-popover-shows-an-author-s-b.md -->
<!-- version: 1.0.0 -->
<!-- guid: 37a8380a-8a25-4024-9556-100ed72a7768 -->
<!-- last-edited: 2026-09-10 -->

# TASK-304 — Author-merge preview popover shows an author's book list as empty on fetch failure, which can bias a merge decision (WEB-04)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `WEB-04` (audit_web.json)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Opus-class · web subagent · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: Wave 3 audit finding `WEB-04` (audit_web.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/path/to/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-304" -b agent/web-304-author-merge-preview-popover-shows-an-au origin/main
cd "$REPO/.worktrees/web-304"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add an error state to `AuthorBooksPopover`; on any rejected/failed fetch, show 'Could not load N of M authors' next to counts rather than silently omitting books, and consider disabling the merge button until the fetch succeeds or is explicitly acknowledged.

Why it matters: This popover exists specifically so a reviewer can eyeball what they're about to merge before calling `api.mergeAuthors` (Authors.tsx:570). If the books fetch for one of the merge candidates fails, the popover under-reports that author's book count with no warning, which can make a real, populated author look like a safe/empty duplicate to merge away. Project memory already tracks author-merge as a fragile area (`project_author_data_state.md`: CreateAuthor racy, ~212 dangling AuthorIDs unrepaired) -- this compounds that risk by feeding a human decision bad data silently.

## Background (verify before editing)

- DedupAuthorTab.tsx:151-174 `AuthorBooksPopover` calls `Promise.all(authorIds.map((id) => api.getBooksByAuthor(id)))` and renders the merged, de-duplicated book list with only a `loading` flag -- no `error`/`books` failure state declared anywhere in the 40-line component (grepped for 'error'/'Error' in the component body: zero matches). `getBooksByAuthor` itself (api.ts:1629-1634) does `if (!response.ok) return [];` -- a 500/timeout looks identical to 'this author candidate has zero books.'
- Anchor: `web/src/components/dedup/DedupAuthorTab.tsx:151` (audit `WEB-04`, confidence high, severity high).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -f web/src/components/dedup/DedupAuthorTab.tsx   # the file the finding is anchored to still exists
  sed -n '145,157p' web/src/components/dedup/DedupAuthorTab.tsx   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `web/src/components/dedup/DedupAuthorTab.tsx` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_web_304.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- A test proving the dry-run / guard path writes nothing (fail-closed on error).

## How to test

```bash
cd web && npm ci && npm run build && npm test -- --run
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_web_304.md`.

## Commit message

```
fix(web): Author-merge preview popover shows an author's book list as empty on f (WEB-04)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
