<!-- file: docs/agent-tasks/todo-completion/web/TASK-167-make-the-book-detail-page-s-series-field-link-to.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4f1b42c4-181d-427b-b83b-01d7f66da643 -->
<!-- last-edited: 2026-08-21 -->

# TASK-167 — Make the book-detail page's Series field link to a library view filtered by that series, landing at series_index (TODO.md L3161)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · web subagent · **Why:** Same shape and same new-plumbing requirement as the author link task (todo_line 3156) — new URL-param handling in useLibraryQuery.ts/Library.tsx plus a real <a href>. · **Depends on:** TASK-166 · **Wave:** 4

Source: `TODO.md` line 3161 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Series name → library filtered by that series.**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-167-make-the-book-detail-page-s-series-field-link-to" -b agent/web-167-make-the-book-detail-page-s-series-field-link-to origin/main
cd "$REPO/.worktrees/web-167-make-the-book-detail-page-s-series-field-link-to"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Replace the plain-text Series field in BookDetailInfoTab.tsx with a real <a href> linking to `/library?series_id=<id>`, and add series_id URL-param reading to Library.tsx/useLibraryQuery.ts (new work, does not exist today). Per the item's own text, pair this with series_index (the item text says series_index, matching the frontend's series_position field) so the resulting library view can, if the Library page supports it, land on/scroll to the right position in the series — confirm whether the Library page has any existing 'scroll to' or 'sort by series_number then filter' capability before committing to that stretch goal; if not, the minimal correct scope is just the series_id filter link with series_position not wired to anything yet.

## Background (verify before editing)

- series_id is on the payload and ?series_id= is supported by the backend per the item's own text; the frontend has no existing series_id URL-param handling, confirmed by the same grep as todo_line 3156's author_id check.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  sed -n '228,233p' web/src/components/bookdetail/BookDetailInfoTab.tsx   # Series field value built as a template string, pushed into coreFields alongside Author — series is rendered as a plain formatted string, no link
  grep -n 'series_id' internal/server/handlers/audiobooks/handler.go   # hit at L346: httputil.ParseQueryIntPtr(c, \"series_id\") — backend already parses series_id as a dedicated param
  ```

### Reuse — don't invent

- Use `seriesID query-param parsing (backend, already exists)` in `internal/server/handlers/audiobooks/handler.go` (verify: `grep -n 'ParseQueryIntPtr(c, \"series_id\")' internal/server/handlers/audiobooks/handler.go`) — do NOT write a parallel helper.

## Step-by-step

1. In BookDetailInfoTab.tsx, change the Series field to a <Link> (react-router-dom) wrapping the existing formatted 'series_name #position' string, hrefing to `/library?series_id=${book.series_id}`.
2. Add series_id URL-param reading to useLibraryQuery.ts, mirroring the author_id work from todo_line 3156 — implement both in the same hook change if doing both items together.
3. Decide (or ask) whether the resulting library view should additionally sort_by=series_number ascending when arriving via a series_id link, so series_position is visually meaningful even without a literal scroll-to. If yes, set sort=series_number&order=asc in the same URL alongside series_id.
4. Verify a multi-book series links correctly and the resulting filtered view is ordered sensibly.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_167.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A standalone book with no series (series_name empty) must render nothing linkable.
- series_id present but series_name empty (data inconsistency) should still link correctly using the id, not silently fail.

## Tests

- web/src/components/bookdetail/BookDetailInfoTab.test.tsx: assert the Series field renders as an anchor with the correct href.
- web/src/hooks/useLibraryQuery.test.ts: assert ?series_id=7 narrows the API call to series_id=7.

Anti-over-suppression: N/A

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] g
- [ ] r
- [ ] e
- [ ] p
- [ ]  
- [ ] -
- [ ] n
- [ ]  
- [ ] '
- [ ] s
- [ ] e
- [ ] r
- [ ] i
- [ ] e
- [ ] s
- [ ] _
- [ ] i
- [ ] d
- [ ] '
- [ ]  
- [ ] w
- [ ] e
- [ ] b
- [ ] /
- [ ] s
- [ ] r
- [ ] c
- [ ] /
- [ ] h
- [ ] o
- [ ] o
- [ ] k
- [ ] s
- [ ] /
- [ ] u
- [ ] s
- [ ] e
- [ ] L
- [ ] i
- [ ] b
- [ ] r
- [ ] a
- [ ] r
- [ ] y
- [ ] Q
- [ ] u
- [ ] e
- [ ] r
- [ ] y
- [ ] .
- [ ] t
- [ ] s
- [ ]  
- [ ] r
- [ ] e
- [ ] t
- [ ] u
- [ ] r
- [ ] n
- [ ] s
- [ ]  
- [ ] >
- [ ] =
- [ ] 1
- [ ]  
- [ ] h
- [ ] i
- [ ] t
- [ ]  
- [ ] (
- [ ] 0
- [ ]  
- [ ] a
- [ ] t
- [ ]  
- [ ] H
- [ ] E
- [ ] A
- [ ] D
- [ ] )
- [ ] ;
- [ ]  
- [ ] g
- [ ] r
- [ ] e
- [ ] p
- [ ]  
- [ ] -
- [ ] n
- [ ]  
- [ ] '
- [ ] s
- [ ] e
- [ ] r
- [ ] i
- [ ] e
- [ ] s
- [ ] _
- [ ] i
- [ ] d
- [ ] '
- [ ]  
- [ ] w
- [ ] e
- [ ] b
- [ ] /
- [ ] s
- [ ] r
- [ ] c
- [ ] /
- [ ] c
- [ ] o
- [ ] m
- [ ] p
- [ ] o
- [ ] n
- [ ] e
- [ ] n
- [ ] t
- [ ] s
- [ ] /
- [ ] b
- [ ] o
- [ ] o
- [ ] k
- [ ] d
- [ ] e
- [ ] t
- [ ] a
- [ ] i
- [ ] l
- [ ] /
- [ ] B
- [ ] o
- [ ] o
- [ ] k
- [ ] D
- [ ] e
- [ ] t
- [ ] a
- [ ] i
- [ ] l
- [ ] I
- [ ] n
- [ ] f
- [ ] o
- [ ] T
- [ ] a
- [ ] b
- [ ] .
- [ ] t
- [ ] s
- [ ] x
- [ ]  
- [ ] r
- [ ] e
- [ ] t
- [ ] u
- [ ] r
- [ ] n
- [ ] s
- [ ]  
- [ ] >
- [ ] =
- [ ] 1
- [ ]  
- [ ] h
- [ ] i
- [ ] t
- [ ]  
- [ ] (
- [ ] 0
- [ ]  
- [ ] a
- [ ] t
- [ ]  
- [ ] H
- [ ] E
- [ ] A
- [ ] D
- [ ] )
- [ ] ;
- [ ]  
- [ ] n
- [ ] p
- [ ] m
- [ ]  
- [ ] -
- [ ] -
- [ ] p
- [ ] r
- [ ] e
- [ ] f
- [ ] i
- [ ] x
- [ ]  
- [ ] w
- [ ] e
- [ ] b
- [ ]  
- [ ] t
- [ ] e
- [ ] s
- [ ] t
- [ ]  
- [ ] -
- [ ] -
- [ ]  
- [ ] B
- [ ] o
- [ ] o
- [ ] k
- [ ] D
- [ ] e
- [ ] t
- [ ] a
- [ ] i
- [ ] l
- [ ] I
- [ ] n
- [ ] f
- [ ] o
- [ ] T
- [ ] a
- [ ] b
- [ ]  
- [ ] a
- [ ] n
- [ ] d
- [ ]  
- [ ] n
- [ ] p
- [ ] m
- [ ]  
- [ ] -
- [ ] -
- [ ] p
- [ ] r
- [ ] e
- [ ] f
- [ ] i
- [ ] x
- [ ]  
- [ ] w
- [ ] e
- [ ] b
- [ ]  
- [ ] t
- [ ] e
- [ ] s
- [ ] t
- [ ]  
- [ ] -
- [ ] -
- [ ]  
- [ ] u
- [ ] s
- [ ] e
- [ ] L
- [ ] i
- [ ] b
- [ ] r
- [ ] a
- [ ] r
- [ ] y
- [ ] Q
- [ ] u
- [ ] e
- [ ] r
- [ ] y
- [ ]  
- [ ] p
- [ ] a
- [ ] s
- [ ] s
- [ ] ;
- [ ]  
- [ ] n
- [ ] p
- [ ] m
- [ ]  
- [ ] -
- [ ] -
- [ ] p
- [ ] r
- [ ] e
- [ ] f
- [ ] i
- [ ] x
- [ ]  
- [ ] w
- [ ] e
- [ ] b
- [ ]  
- [ ] r
- [ ] u
- [ ] n
- [ ]  
- [ ] l
- [ ] i
- [ ] n
- [ ] t
- [ ]  
- [ ] p
- [ ] a
- [ ] s
- [ ] s
- [ ] e
- [ ] s
- [ ] .
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_167.md`.

## Commit message

```
feat(web): Make the book-detail page's Series field link to a library v (TODO L3161)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run sed -n '228,233p' web/src/components/bookdetail/BookDetailInfoTab.tsx` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Do in the same PR as todo_line 3156 if practical — both need the identical new author_id/series_id URL-param plumbing added to useLibraryQuery.ts, so splitting them risks touching the same hook twice with overlapping diffs.
