<!-- file: docs/agent-tasks/todo-completion/web/TASK-171-make-the-book-detail-page-s-author-field-s-link-.md -->
<!-- version: 1.0.0 -->
<!-- guid: cca9ee6a-2ee5-4520-a3d5-21497c0aa820 -->
<!-- last-edited: 2026-08-21 -->

# TASK-171 — Make the book-detail page's Author field(s) link to a library view filtered by that author (TODO.md L3156)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · web subagent · **Why:** Requires both a new UI affordance (real <a href> per author, per the item's own notes) and new URL-param plumbing in useLibraryQuery.ts/Library.tsx that does not exist yet — more than a trivial link swap. · **Depends on:** none · **Wave:** 3

Source: `TODO.md` line 3156 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Author name → library filtered by that author.**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-171-make-the-book-detail-page-s-author-field-s-link-" -b agent/web-171-make-the-book-detail-page-s-author-field-s-link- origin/main
cd "$REPO/.worktrees/web-171-make-the-book-detail-page-s-author-field-s-link-"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

In BookDetailInfoTab.tsx, replace the single joined author string with one real `<a href>` (React Router `<Link>`) per entry in book.authors[] (each carrying its own id, name, role, position — a multi-author book must link each contributor separately, not just the primary), targeting `/library?author_id=<id>` (or a hash-router equivalent matching this app's routing). Add author_id URL-param reading to Library.tsx/useLibraryQuery.ts so landing on that URL actually narrows the list — this param handling does not exist today and is new work, not reuse.

## Background (verify before editing)

- The book payload already carries author_id plus an authors[] array with id, name, role, and position per the item's own text — confirmed present in BookDetailInfoTab.tsx's use of book.authors[].name.
- The Library page's filter state currently narrows via the `filters` JSON param (buildFieldFilters in Library.tsx) for text fields like author/series/genre/language, and via dedicated int query params (author_id, series_id) that the BACKEND already parses but the FRONTEND never reads or sets.
- The page also applies is_primary_version=true by default — an author link that inherits that default hides non-primary copies, which the item's own notes flag as worth being deliberate about rather than accidental.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  sed -n '213,226p' web/src/components/bookdetail/BookDetailInfoTab.tsx   # authorVal built from book.authors[].name, pushed into coreFields, rendered later via <Typography>{item.value}</Typography> — author is rendered as plain, non-linked text
  grep -n 'author_id' internal/server/handlers/audiobooks/handler.go   # hit at L345: httputil.ParseQueryIntPtr(c, \"author_id\") — the backend already accepts author_id as a filter param
  grep -n 'author_id' web/src/pages/Library.tsx web/src/hooks/useLibraryQuery.ts   # 0 hits in both files — the frontend has no existing author_id URL-param handling
  ```

### Reuse — don't invent

- Use `authorID query-param parsing (backend, already exists)` in `internal/server/handlers/audiobooks/handler.go` (verify: `grep -n 'ParseQueryIntPtr(c, \"author_id\")' internal/server/handlers/audiobooks/handler.go`) — do NOT write a parallel helper.

## Step-by-step

1. In web/src/components/bookdetail/BookDetailInfoTab.tsx, find the coreFields array construction (~line 222-227) and change the Author entry from a single joined string to a list of {name, id} entries rendered as separate <Link> elements (import Link from 'react-router-dom', matching whatever router the rest of the app already uses — check web/src/App.tsx or an existing <Link> usage for the exact import/base-path convention).
2. Each author link's href should be `/library?author_id=${author.id}` — grep the existing route path for the library page (likely '/' or '/library') to get the exact base path right.
3. In web/src/hooks/useLibraryQuery.ts, add reading of `searchParams.get('author_id')` (mirroring how 'search'/'sort'/'tag' are already read in Library.tsx) and pass it through to the API call as the dedicated `author_id` query param (NOT via the filters= JSON mechanism, since that's a separate code path from the dedicated int param the backend already parses).
4. Confirm the is_primary_version=true default still applies unless the item's design intent is to show all versions when arriving via an author link — if undecided, keep the existing default (safer, matches current browsing behavior) and note the tradeoff in a code comment.
5. Verify with the running app (webapp-testing skill / Playwright) that clicking an author name on a multi-author book navigates to a library view showing only that author's books, and that a middle-click / open-in-new-tab works (real <a href>, no onClick-only handler).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_171.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book with author_name set but no authors[] array (legacy data) must still render something sensible — either a plain-text fallback with no link, or a link using author_name as a text-match filter field instead of author_id.
- Empty/unknown author (no authors[], no author_name) must render nothing linkable, not a link to author_id=undefined.

## Tests

- web/src/components/bookdetail/BookDetailInfoTab.test.tsx: assert each author renders as a real anchor with the correct href for a multi-author book fixture.
- web/src/hooks/useLibraryQuery.test.ts: assert a URL with ?author_id=42 results in the API call including author_id=42.
- Playwright E2E (test-e2e or webapp-testing skill): click an author link on a book detail page, land on Library, assert only that author's books are shown.

Anti-over-suppression test: `N/A — not a filter/guard task.` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] npm --prefix web run lint && npm --prefix web test passes.
- [ ] A book fixture with 2+ authors renders 2+ separate <a> elements, not one joined string.
- [ ] Anti-over-suppression test: `N/A — not a filter/guard task.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_171.md`.

## Commit message

```
feat(web): Make the book-detail page's Author field(s) link to a librar (TODO L3156)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`npm --prefix web run lint && npm --prefix web test passes.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Prefer real <a href> per the item's own notes (middle-clickable, shareable). Pairs naturally with todo_line 3161 (series link) since both need the same new URL-param plumbing in useLibraryQuery.ts — consider doing them in the same PR to avoid touching that hook twice.
