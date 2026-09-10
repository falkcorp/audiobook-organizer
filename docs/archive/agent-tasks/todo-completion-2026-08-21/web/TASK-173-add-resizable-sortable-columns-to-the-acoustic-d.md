<!-- file: docs/agent-tasks/todo-completion/web/TASK-173-add-resizable-sortable-columns-to-the-acoustic-d.md -->
<!-- version: 1.1.0 -->
<!-- guid: 01f9b950-f16e-4929-b1c9-92fc3faf7184 -->
<!-- last-edited: 2026-09-02 -->

# TASK-173 — Add resizable/sortable columns to the acoustic dedup candidates table (TODO.md L10660)

> **Status 2026-09-02:** 🟡 OPEN — still worth doing — grep useConfigurableTable DedupAcousticTab.tsx = 0 hits; raw <TableContainer> still at :1136-1307 with candidates.map at :1163; no commits to the file since 2026-08-21. Recommendation: keep.

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · web subagent · **Why:** requires preserving the checkbox-select-all column and busy/selected row styling while swapping in ConfigurableTable's column model — not pure mechanical copy · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 10660 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**1.16 — resizable/sortable columns** (H1:2750) — " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-15.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-173-add-resizable-sortable-columns-to-the-acoustic-d" -b agent/web-173-add-resizable-sortable-columns-to-the-acoustic-d origin/main
cd "$REPO/.worktrees/web-173-add-resizable-sortable-columns-to-the-acoustic-d"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Convert the acoustic-dedup candidates table (web/src/components/dedup/DedupAcousticTab.tsx, the `<TableContainer>` block starting ~L1136 inside AcousticDedupTab) to use `useConfigurableTable`/`ResizableHeaderCell` from web/src/components/common/ConfigurableTable.tsx, giving it resizable + sortable columns exactly like WriteBackPreviewTable.tsx, while keeping the existing checkbox-select-all and per-row busy/selected styling intact.

## Background (verify before editing)

- WriteBackPreviewTable.tsx is the canonical reuse example: it calls `useConfigurableTable({storageKey, columns, defaultSortField, defaultSortDir})` and renders `<ResizableHeaderCell>` per column plus `sortRows(rows)` before mapping (verify: `grep -n sortRows web/src/components/settings/WriteBackPreviewTable.tsx`).
- ColumnDef<T> supports a custom `render(row) => ReactNode` per column and an optional `sortValue(row) => string|number` (web/src/components/common/ConfigurableTable.tsx:23-42) — the checkbox column can be declared with `sortable: false` and a `render` that renders the existing <Checkbox> JSX unchanged.
- Column widths/visibility persist to localStorage keyed by `storageKey` (ConfigurableTable.tsx:104, `${STORAGE_KEYS.TABLE_CONFIG}_${storageKey}`) — pick a unique storageKey such as 'dedup-acoustic-candidates'.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'TableContainer\|ConfigurableTable' web/src/components/dedup/DedupAcousticTab.tsx   # hits at L1136-1137, no ConfigurableTable import — AcousticDedupTab candidates table uses plain MUI Table, not ConfigurableTable
  grep -n 'candidates.map((c) =>' web/src/components/dedup/DedupAcousticTab.tsx   # 1 hit ~L1163 — rows are non-expandable, one candidate per row
  ```

### Reuse — don't invent

- Use `useConfigurableTable hook + ResizableHeaderCell` in `web/src/components/common/ConfigurableTable.tsx` (verify: `grep -n 'export function useConfigurableTable\|export function ResizableHeaderCell' web/src/components/common/ConfigurableTable.tsx`) — do NOT write a parallel helper.
- Use `reference integration pattern` in `web/src/components/settings/WriteBackPreviewTable.tsx` (verify: `grep -n useConfigurableTable web/src/components/settings/WriteBackPreviewTable.tsx`) — do NOT write a parallel helper.

## Step-by-step

1. Import `useConfigurableTable`, `ResizableHeaderCell`, and `ColumnDef` from '../common/ConfigurableTable' in DedupAcousticTab.tsx.
2. Define a `columns: ColumnDef<DedupCandidate>[]` array outside/above the AcousticDedupTab component (or via useMemo inside it) covering: select-checkbox (sortable:false, custom render), Book A (sortValue by book title via bookCache lookup), Book B (same), Similarity (sortValue: c.similarity ?? 0), Actions (sortable:false).
3. Call `const table = useConfigurableTable({storageKey: 'dedup-acoustic-candidates', columns, defaultSortField: 'similarity', defaultSortDir: 'desc'})` inside AcousticDedupTab.
4. Replace the `<TableHead><TableRow>` static `<TableCell>` headers (L1138-1159) with a map over `table.visibleColumns` rendering `<ResizableHeaderCell>` per WriteBackPreviewTable's pattern (verify exact JSX with `grep -n ResizableHeaderCell -A15 web/src/components/settings/WriteBackPreviewTable.tsx`).
5. Replace `candidates.map((c) => ...)` (L1163) with `table.sortRows(candidates).map((c) => ...)`, keeping the existing per-row `<TableRow selected sx={...}>` wrapper and cell content, but iterate `table.visibleColumns` to decide which cells to render (or keep static cells gated by `table.isColumnVisible(key)` if a full generic-row-render refactor is out of scope for this pass).
6. Bump the file-header version comment at the top of DedupAcousticTab.tsx.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_173.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Empty candidates list — table should render header only, no crash on `table.sortRows([])`.
- A candidate whose bookCache entry hasn't loaded yet (async) — sortValue must handle undefined book gracefully (fallback to empty string, not throw).

## Tests

- web/src/components/dedup/__tests__/DedupAcousticTab.test.tsx (create if absent — check `find web/src -iname 'DedupAcousticTab*test*'` first) — assert clicking a column header toggles sort direction and the candidate order changes accordingly (render with 3 fixture candidates of differing similarity, click 'Similarity' header, assert new row order).
- Anti-over-suppression: a test asserting the checkbox column's select-all/select-one behavior still works after the refactor (no regression in `selectedCandIds` state).

Anti-over-suppression test: `test: 'select-all checkbox still selects all visible rows after ConfigurableTable conversion'` — a known-good input still passes with the new guard active.

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
- [ ] A
- [ ] c
- [ ] o
- [ ] u
- [ ] s
- [ ] t
- [ ] i
- [ ] c
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
- [ ] a
- [ ] c
- [ ] o
- [ ] u
- [ ] s
- [ ] t
- [ ] i
- [ ] c
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
- [ ] A
- [ ] c
- [ ] o
- [ ] u
- [ ] s
- [ ] t
- [ ] i
- [ ] c
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
- [ ]  
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
- [ ] .
- [ ] m
- [ ] a
- [ ] p
- [ ] (
- [ ] (
- [ ] c
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
- [ ] 1
- [ ] 1
- [ ] 6
- [ ] 3
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
- [ ] D
- [ ] e
- [ ] d
- [ ] u
- [ ] p
- [ ] A
- [ ] c
- [ ] o
- [ ] u
- [ ] s
- [ ] t
- [ ] i
- [ ] c
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
- [ ] s
- [ ] e
- [ ] l
- [ ] e
- [ ] c
- [ ] t
- [ ] -
- [ ] a
- [ ] l
- [ ] l
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
- [ ] Anti-over-suppression test: `test: 'select-all checkbox still selects all visible rows after ConfigurableTable conversion'` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_173.md`.

## Commit message

```
feat(web): Add resizable/sortable columns to the acoustic dedup candida (TODO L10660)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -n 'TableContainer\|ConfigurableTable' web/src/components/dedup/DedupAcousticTab.tsx` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

The smaller per-candidate segment-detail table in the same file (L461-514, 4 fixed columns: Segment/Book A fingerprint/Book B fingerprint/Match) is a nested detail view inside a compare panel, not a list of many rows — resizable/sortable columns add little value there; left out of this task's scope deliberately (small, low-value, would need its own decision if pursued).
