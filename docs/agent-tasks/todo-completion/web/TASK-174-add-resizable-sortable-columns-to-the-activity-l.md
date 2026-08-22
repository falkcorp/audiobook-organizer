<!-- file: docs/agent-tasks/todo-completion/web/TASK-174-add-resizable-sortable-columns-to-the-activity-l.md -->
<!-- version: 1.0.0 -->
<!-- guid: e4116724-55c9-4f64-97fa-7577cfe021dd -->
<!-- last-edited: 2026-08-21 -->

# TASK-174 — Add resizable/sortable columns to the Activity Log table (TODO.md L10660)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Sonnet-class · web subagent · **Why:** row bodies are heterogeneous (plain/batched/digest) so only the header (resize+visibility) can reuse ConfigurableTable directly; sort must be applied to the pre-render `entries` array by a chosen field before any of the three row-render branches run, which needs care to not break existing digest-expansion state · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 10660 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**1.16 — resizable/sortable columns** (H1:2750) — " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-15.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-174-add-resizable-sortable-columns-to-the-activity-l" -b agent/web-174-add-resizable-sortable-columns-to-the-activity-l origin/main
cd "$REPO/.worktrees/web-174-add-resizable-sortable-columns-to-the-activity-l"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Give the Activity Log's entries table resizable columns (Time/Level/Type/Summary/Source/Tags) via `useConfigurableTable`'s header machinery, and sortable columns by wrapping the existing `entries` array with `table.sortRows()` before the three-way row-type branch (plain/batched/digest) runs, without altering the batched/digest row rendering logic itself.

## Background (verify before editing)

- The header row (web/src/pages/ActivityLog.tsx L2093-2100) is static TableCells for Time/Level/Type/Summary/(Source)/(Tags), with Source and Tags conditionally hidden on mobile (`!isMobile &&`).
- Row rendering branches three ways inside `entries.map((entry) => {...})` (L2105+): batched entries render `<BatchActivityEntry>`, digest-tier entries render an expand/collapse summary, everything else presumably renders a plain row further down in the same map callback.
- ConfigurableTable's `sortRows()` only needs a `sortValue(row) => string|number` per column and works on any array — it does not require uniform row rendering, so it can be used purely for sort+resize while leaving the three-way row branch untouched.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'TableContainer\|<TableCell>Time' web/src/pages/ActivityLog.tsx   # hits at L2091-2093 — ActivityLog table is a plain MUI Table with static TableCell headers
  grep -n 'BatchActivityEntry\|entry.tier ===' web/src/pages/ActivityLog.tsx   # ≥2 hits ~L2105-2115 — rows branch into 3 shapes: plain, batched, digest
  ```

### Reuse — don't invent

- Use `useConfigurableTable + ResizableHeaderCell (header-only usage)` in `web/src/components/common/ConfigurableTable.tsx` (verify: `grep -n 'export function' web/src/components/common/ConfigurableTable.tsx`) — do NOT write a parallel helper.

## Step-by-step

1. Import `useConfigurableTable`, `ResizableHeaderCell`, `ColumnDef` from '../components/common/ConfigurableTable' into ActivityLog.tsx.
2. Define `columns: ColumnDef<ActivityEntry>[]` for Time (sortValue: entry.timestamp), Level (sortValue: entry.tier or level field — check the entry type's actual field name via `grep -n 'interface ActivityEntry' web/src/pages/ActivityLog.tsx` or wherever it's imported from), Type, Summary, Source, Tags — mark Summary/Source/Tags as `sortable: false` if they don't have a sensible single-value sort (Tags is an array).
3. Call `const table = useConfigurableTable({storageKey: 'activity-log', columns, defaultSortField: 'time', defaultSortDir: 'desc'})`.
4. Replace the static header `<TableRow>` (L2093-2100) with a map over `table.visibleColumns` using `<ResizableHeaderCell>`, preserving the `!isMobile &&` conditional visibility (either via column `defaultVisible` gated on `isMobile`, recomputed with a `useMemo` keyed on `isMobile`, or by leaving Source/Tags always in `columns` and letting the user's own visibility toggle handle small screens).
5. Change `entries.map((entry) => {...})` to `table.sortRows(entries).map((entry) => {...})`, leaving the batched/digest/plain branch logic inside the callback completely unchanged.
6. Bump the file-header version comment at the top of ActivityLog.tsx.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_174.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A batched entry's summary field may be synthesized/absent — sortValue for Summary must handle undefined without throwing.
- Sorting must not desync from the live-refresh polling (existing 'stale data' warning banner at L2087) — sort is applied client-side per render, so a background refresh naturally reflows through the same sortRows() call; no extra care needed beyond not caching a stale sorted copy in state.

## Tests

- web/src/pages/__tests__/ActivityLog.test.tsx (check `find web/src -iname 'ActivityLog*test*'` for existing coverage first) — add a case asserting clicking the 'Time' header reverses row order, and that batched/digest rows still render correctly after a sort is applied.

Anti-over-suppression test: `test: 'digest-entry expand/collapse still works after entries are sorted by a resizable-column header click'` — a known-good input still passes with the new guard active.

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
- [ ] p
- [ ] a
- [ ] g
- [ ] e
- [ ] s
- [ ] /
- [ ] A
- [ ] c
- [ ] t
- [ ] i
- [ ] v
- [ ] i
- [ ] t
- [ ] y
- [ ] L
- [ ] o
- [ ] g
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
- [ ] a
- [ ] c
- [ ] t
- [ ] i
- [ ] v
- [ ] i
- [ ] t
- [ ] y
- [ ] -
- [ ] l
- [ ] o
- [ ] g
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
- [ ] e
- [ ] n
- [ ] t
- [ ] r
- [ ] i
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
- [ ]  
- [ ] (
- [ ] e
- [ ] n
- [ ] t
- [ ] r
- [ ] i
- [ ] e
- [ ] s
- [ ] .
- [ ] m
- [ ] a
- [ ] p
- [ ] (
- [ ] (
- [ ] e
- [ ] n
- [ ] t
- [ ] r
- [ ] y
- [ ] )
- [ ]  
- [ ] =
- [ ] >
- [ ]  
- [ ] i
- [ ] s
- [ ]  
- [ ] a
- [ ] t
- [ ]  
- [ ] L
- [ ] 2
- [ ] 1
- [ ] 0
- [ ] 5
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
- [ ] <
- [ ] T
- [ ] a
- [ ] b
- [ ] l
- [ ] e
- [ ] C
- [ ] e
- [ ] l
- [ ] l
- [ ] >
- [ ] T
- [ ] i
- [ ] m
- [ ] e
- [ ] <
- [ ] /
- [ ] T
- [ ] a
- [ ] b
- [ ] l
- [ ] e
- [ ] C
- [ ] e
- [ ] l
- [ ] l
- [ ] >
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
- [ ] 0
- [ ]  
- [ ] h
- [ ] i
- [ ] t
- [ ] s
- [ ]  
- [ ] (
- [ ] 1
- [ ]  
- [ ] a
- [ ] t
- [ ]  
- [ ] H
- [ ] E
- [ ] A
- [ ] D
- [ ] ,
- [ ]  
- [ ] L
- [ ] 2
- [ ] 0
- [ ] 9
- [ ] 5
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
- [ ] A
- [ ] c
- [ ] t
- [ ] i
- [ ] v
- [ ] i
- [ ] t
- [ ] y
- [ ] L
- [ ] o
- [ ] g
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
- [ ] d
- [ ] i
- [ ] g
- [ ] e
- [ ] s
- [ ] t
- [ ]  
- [ ] e
- [ ] x
- [ ] p
- [ ] a
- [ ] n
- [ ] d
- [ ] /
- [ ] c
- [ ] o
- [ ] l
- [ ] l
- [ ] a
- [ ] p
- [ ] s
- [ ] e
- [ ]  
- [ ] r
- [ ] e
- [ ] g
- [ ] r
- [ ] e
- [ ] s
- [ ] s
- [ ] i
- [ ] o
- [ ] n
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
- [ ] Anti-over-suppression test: `test: 'digest-entry expand/collapse still works after entries are sorted by a resizable-column header click'` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_174.md`.

## Commit message

```
feat(web): Add resizable/sortable columns to the Activity Log table (TODO L10660)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -n 'TableContainer\|<TableCell>Time' web/src/pages/ActivityLog.tsx` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Larger/riskier than the WriteBackPreviewTable pattern because of the three-way row branch; if a cold Haiku agent struggles with the header-visibility/mobile interaction in step 4, it is acceptable to keep Source/Tags columns always declared and rely on ConfigurableTable's own visibility toggle instead of the old isMobile conditional — flag that simplification in the PR description rather than guessing silently.
