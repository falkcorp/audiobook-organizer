<!-- file: docs/superpowers/fleet-tasks/036-fe-1-16-resizable-columns.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5456a0dd-444f-4fcf-a477-853dd1d3a7f3 -->
<!-- last-edited: 2026-08-22 -->

# Task 036: 1.16 — Resizable + dynamically-sortable columns everywhere

**Depends on:** none
**Estimated effort:** L
**Wave:** 10 (UI polish)

## Goal

Every table on every page gets draggable column dividers and click-to-sort headers, with
widths and sort state persisted to localStorage. Build a single `ResizableSortableTable`
component (or extend `ConfigurableTable`) and roll it across all pages.

## Context

- Some pages already have resizable columns (check `ConfigurableTable` or similar) — find
  what exists first: `grep -rn "resizable\|ResizableColumn\|ConfigurableTable" web/src/`
- Pages that need it: library, dedup, activity, iTunes write-back preview, metadata review
- localStorage key convention: `table_config_{page}_{column}` (check `lib/storageKeys.ts`)
- MUI DataGrid has built-in column resizing — check if the project already uses it
- The library grid uses a custom grid, not MUI DataGrid — check `LibraryBookGrid.tsx`

## Files to create/modify

- `web/src/components/common/ResizableSortableTable.tsx` (new, or extend existing)
- `web/src/pages/Library.tsx` — wire if needed
- `web/src/pages/Activity.tsx` — wire
- `web/src/pages/BookDedup.tsx` — wire
- `web/src/components/audiobooks/MetadataReviewDialog.tsx` — wire candidate list
- iTunes write-back preview table — wire

## Instructions

### 1. Audit existing column infrastructure

```bash
grep -rn "ConfigurableTable\|ResizableColumn\|columnWidth\|sortable" web/src/ | grep -v node_modules | head -30
```

### 2. Build `ResizableSortableTable` (if not existing)

Wrapper around MUI `Table` with:
- Column resize handles (CSS `resize: horizontal` on `<th>` cells, or drag listeners)
- Sort chevrons on column headers (click cycles: none → asc → desc)
- `useLocalStorage` hook to persist widths + sort state per `tableKey` prop

```tsx
interface ResizableSortableTableProps<T> {
    tableKey: string;         // localStorage key prefix
    columns: ColumnDef<T>[];  // {id, label, render, sortKey?, defaultWidth?}
    rows: T[];
    defaultSort?: {column: string; direction: 'asc' | 'desc'};
}
```

Sort is client-side for tables with < 1000 rows; for larger tables, raise a `onSortChange`
callback and let the parent handle server-side sort.

### 3. Roll across pages

For each page, replace the current table rendering with `<ResizableSortableTable>`.
Use unique `tableKey` values: `"library"`, `"activity"`, `"dedup"`, `"metadata-review"`.

### 4. Preserve existing functionality

Do NOT break existing features — filtering, selection, bulk actions, pagination.
The component should be a drop-in replacement for the existing table markup.

## Test

```bash
npm test
make ci
```

Manual: resize a column in the library, refresh — verify width persists. Click a column
header — verify sort works. Test on every listed page.

## Commit

```
feat(ui): resizable + sortable columns on all table pages, persist to localStorage (1.16)
```

## PR title

`feat(ui): resizable and sortable columns everywhere — 1.16`

## After merging

Mark `- [ ] **1.16**` as `- [x]` in `TODO.md`.
