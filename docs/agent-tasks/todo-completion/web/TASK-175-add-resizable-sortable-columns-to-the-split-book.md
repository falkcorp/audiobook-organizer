<!-- file: docs/agent-tasks/todo-completion/web/TASK-175-add-resizable-sortable-columns-to-the-split-book.md -->
<!-- version: 1.0.0 -->
<!-- guid: e947bd1a-2a05-485d-944f-6d2c6c86bef8 -->
<!-- last-edited: 2026-08-21 -->

# TASK-175 — Add resizable/sortable columns to the split-book dedup candidates table (TODO.md L10660)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · web subagent · **Why:** header-only ConfigurableTable integration plus sorting the already-paginated `candidates` array before `pageSlice` is computed — needs care that sort doesn't fight the existing client-side pagination state · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 10660 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**1.16 — resizable/sortable columns** (H1:2750) — " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-15.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-175-add-resizable-sortable-columns-to-the-split-book" -b agent/web-175-add-resizable-sortable-columns-to-the-split-book origin/main
cd "$REPO/.worktrees/web-175-add-resizable-sortable-columns-to-the-split-book"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Give the split-book detector's candidates table (columns: Parent folder / Suggested title / Suggested author / Books / Actions) resizable + sortable headers via ConfigurableTable, applying `table.sortRows()` to the `candidates` array before `pageSlice` is derived, while leaving the expandable `<CandidateRow>` row component and `<TablePagination>` untouched.

## Background (verify before editing)

- DedupSplitBookTab.tsx L326-368: `pageSlice.map((c) => <CandidateRow key={c.id} candidate={c} expanded={expanded.has(c.id)} onToggle={...} ... />)` — find where `pageSlice` is derived from `candidates` (likely `candidates.slice(page*rowsPerPage, ...)`) via `grep -n pageSlice web/src/components/dedup/DedupSplitBookTab.tsx`.
- TablePagination props (count, page, rowsPerPage) already reference `candidates.length` (L370) so sorting the underlying array before pagination is applied keeps page boundaries correct.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'CandidateRow\|TablePagination' web/src/components/dedup/DedupSplitBookTab.tsx   # hits at L358-365 and ~L369 — split-book table uses a custom expandable CandidateRow, not plain cells
  ```

### Reuse — don't invent

- Use `useConfigurableTable header-only usage (same pattern as part 4)` in `web/src/components/common/ConfigurableTable.tsx` (verify: `grep -n 'export function useConfigurableTable' web/src/components/common/ConfigurableTable.tsx`) — do NOT write a parallel helper.

## Step-by-step

1. Import `useConfigurableTable`, `ResizableHeaderCell`, `ColumnDef` in DedupSplitBookTab.tsx.
2. Define columns for the 5 header cells (Parent folder, Suggested title, Suggested author, Books count, Actions — mark Actions `sortable: false`), with sortValue reading `c.parent_folder`, `c.suggested_title`, `c.suggested_author`, `c.book_ids.length` respectively.
3. Call `const table = useConfigurableTable({storageKey: 'dedup-split-book-candidates', columns, defaultSortField: 'parent_folder', defaultSortDir: 'asc'})`.
4. Replace the static `<TableHead><TableRow>` (with the leading blank `<TableCell />` for the expand toggle kept as-is, non-configurable) with a map over `table.visibleColumns` rendering `<ResizableHeaderCell>`.
5. Find the `pageSlice` derivation and wrap its source with `table.sortRows(candidates)` before slicing, e.g. `const sorted = table.sortRows(candidates); const pageSlice = sorted.slice(page * rowsPerPage, (page + 1) * rowsPerPage);` — locate the exact current expression via `grep -n 'pageSlice =' web/src/components/dedup/DedupSplitBookTab.tsx`.
6. Bump the file-header version comment.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_175.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Sorting while a row is expanded (`expanded.has(c.id)`) — the Set is keyed by candidate id, not row index, so expand state survives resorting automatically; verify no regression.

## Tests

- web/src/components/dedup/__tests__/DedupSplitBookTab.test.tsx (check `find web/src/components/dedup -iname 'DedupSplitBookTab*test*'` for existing coverage) — add a case asserting a header click re-sorts candidates and pagination page resets/stays consistent (e.g. clicking a header while on page 2 either resets to page 0 or keeps showing valid rows, whichever the existing pagination component does by default — assert current behavior, don't invent new behavior).

Anti-over-suppression test: `test: 'expanded row state survives a column-header sort'` — a known-good input still passes with the new guard active.

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
- [ ] u
- [ ] s
- [ ] e
- [ ] C
- [ ] o
- [ ] n
- [ ] f
- [ ] i
- [ ] g
- [ ] u
- [ ] r
- [ ] a
- [ ] b
- [ ] l
- [ ] e
- [ ] T
- [ ] a
- [ ] b
- [ ] l
- [ ] e
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
- [ ] d
- [ ] e
- [ ] d
- [ ] u
- [ ] p
- [ ] /
- [ ] D
- [ ] e
- [ ] d
- [ ] u
- [ ] p
- [ ] S
- [ ] p
- [ ] l
- [ ] i
- [ ] t
- [ ] B
- [ ] o
- [ ] o
- [ ] k
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
- [ ] g
- [ ] r
- [ ] e
- [ ] p
- [ ]  
- [ ] -
- [ ] n
- [ ]  
- [ ] "
- [ ] s
- [ ] t
- [ ] o
- [ ] r
- [ ] a
- [ ] g
- [ ] e
- [ ] K
- [ ] e
- [ ] y
- [ ] :
- [ ]  
- [ ] '
- [ ] d
- [ ] e
- [ ] d
- [ ] u
- [ ] p
- [ ] -
- [ ] s
- [ ] p
- [ ] l
- [ ] i
- [ ] t
- [ ] -
- [ ] b
- [ ] o
- [ ] o
- [ ] k
- [ ] -
- [ ] c
- [ ] a
- [ ] n
- [ ] d
- [ ] i
- [ ] d
- [ ] a
- [ ] t
- [ ] e
- [ ] s
- [ ] '
- [ ] "
- [ ]  
- [ ] r
- [ ] e
- [ ] t
- [ ] u
- [ ] r
- [ ] n
- [ ] s
- [ ]  
- [ ] 1
- [ ]  
- [ ] h
- [ ] i
- [ ] t
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
- [ ] t
- [ ] a
- [ ] b
- [ ] l
- [ ] e
- [ ] .
- [ ] s
- [ ] o
- [ ] r
- [ ] t
- [ ] R
- [ ] o
- [ ] w
- [ ] s
- [ ] (
- [ ] c
- [ ] a
- [ ] n
- [ ] d
- [ ] i
- [ ] d
- [ ] a
- [ ] t
- [ ] e
- [ ] s
- [ ] )
- [ ] '
- [ ]  
- [ ] r
- [ ] e
- [ ] t
- [ ] u
- [ ] r
- [ ] n
- [ ] s
- [ ]  
- [ ] 1
- [ ]  
- [ ] h
- [ ] i
- [ ] t
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
- [ ] R
- [ ] e
- [ ] s
- [ ] i
- [ ] z
- [ ] a
- [ ] b
- [ ] l
- [ ] e
- [ ] H
- [ ] e
- [ ] a
- [ ] d
- [ ] e
- [ ] r
- [ ] C
- [ ] e
- [ ] l
- [ ] l
- [ ] '
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
- [ ] D
- [ ] e
- [ ] d
- [ ] u
- [ ] p
- [ ] S
- [ ] p
- [ ] l
- [ ] i
- [ ] t
- [ ] B
- [ ] o
- [ ] o
- [ ] k
- [ ] T
- [ ] a
- [ ] b
- [ ]  
- [ ] p
- [ ] a
- [ ] s
- [ ] s
- [ ] e
- [ ] s
- [ ]  
- [ ] i
- [ ] n
- [ ] c
- [ ] l
- [ ] u
- [ ] d
- [ ] i
- [ ] n
- [ ] g
- [ ]  
- [ ] t
- [ ] h
- [ ] e
- [ ]  
- [ ] e
- [ ] x
- [ ] p
- [ ] a
- [ ] n
- [ ] d
- [ ] e
- [ ] d
- [ ] -
- [ ] r
- [ ] o
- [ ] w
- [ ] -
- [ ] s
- [ ] u
- [ ] r
- [ ] v
- [ ] i
- [ ] v
- [ ] e
- [ ] s
- [ ] -
- [ ] s
- [ ] o
- [ ] r
- [ ] t
- [ ]  
- [ ] t
- [ ] e
- [ ] s
- [ ] t
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
- [ ] Anti-over-suppression test: `test: 'expanded row state survives a column-header sort'` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_175.md`.

## Commit message

```
feat(web): Add resizable/sortable columns to the split-book dedup candi (TODO L10660)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -n 'CandidateRow\|TablePagination' web/src/components/dedup/DedupSplitBookTab.tsx` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Same header-only integration pattern as part 4 (Activity Log) — both tables have non-uniform row rendering that rules out a full drop-in ConfigurableTable body swap.
