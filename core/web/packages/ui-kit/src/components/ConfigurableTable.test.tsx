// file: core/web/packages/ui-kit/src/components/ConfigurableTable.test.tsx
// version: 0.1.0
// guid: 48aee08a-24d1-4deb-9489-27159ac268b0
// last-edited: 2026-11-21

import '@testing-library/jest-dom/vitest';
import { act, fireEvent, render, renderHook, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ColumnPicker, ResizableHeaderCell, useConfigurableTable, type ColumnDef } from './ConfigurableTable';

interface Row {
  name: string;
  floor: number;
}
const KEY = 'test-app.table.villains';
const COLS: ColumnDef<Row>[] = [
  { key: 'name', label: 'Name', render: (r) => r.name, sortValue: (r) => r.name },
  { key: 'floor', label: 'Floor', defaultWidth: 90, render: (r) => String(r.floor), sortValue: (r) => r.floor },
  { key: 'notes', label: 'Notes', defaultVisible: false, render: () => '' },
];
const ROWS: Row[] = [
  { name: 'Penguin', floor: 3 },
  { name: 'Bane', floor: 10 },
  { name: 'Riddler', floor: 2 },
];

beforeEach(() => localStorage.clear());

describe('useConfigurableTable', () => {
  it('starts with the default-visible columns and default widths', () => {
    const { result } = renderHook(() => useConfigurableTable({ storageKey: KEY, columns: COLS }));
    expect(result.current.visibleColumns.map((c) => c.key)).toEqual(['name', 'floor']);
    expect(result.current.columnWidths).toMatchObject({ name: 150, floor: 90, notes: 150 });
  });

  it('persists under exactly the given key, and writes nothing else', () => {
    const { result } = renderHook(() => useConfigurableTable({ storageKey: KEY, columns: COLS }));
    act(() => result.current.toggleColumn('notes'));
    expect(JSON.parse(localStorage.getItem(KEY)!).visible).toEqual(['name', 'floor', 'notes']);
    expect(localStorage.length).toBe(1);
  });

  it('restores a stored layout, dropping a column that no longer exists and adding a missing width', () => {
    localStorage.setItem(KEY, JSON.stringify({ visible: ['floor', 'gone'], widths: { floor: 200 } }));
    const { result } = renderHook(() => useConfigurableTable({ storageKey: KEY, columns: COLS }));
    expect(result.current.visibleColumns.map((c) => c.key)).toEqual(['floor']);
    expect(result.current.columnWidths).toMatchObject({ floor: 200, name: 150 });
  });

  it('ignores a corrupt stored layout', () => {
    localStorage.setItem(KEY, '{nope');
    const { result } = renderHook(() => useConfigurableTable({ storageKey: KEY, columns: COLS }));
    expect(result.current.visibleColumns.map((c) => c.key)).toEqual(['name', 'floor']);
  });

  it('never hides the last visible column', () => {
    const { result } = renderHook(() => useConfigurableTable({ storageKey: KEY, columns: COLS }));
    act(() => result.current.toggleColumn('floor'));
    act(() => result.current.toggleColumn('name'));
    expect(result.current.visibleColumns.map((c) => c.key)).toEqual(['name']);
  });

  it('resets to the defaults', () => {
    const { result } = renderHook(() => useConfigurableTable({ storageKey: KEY, columns: COLS }));
    act(() => result.current.toggleColumn('notes'));
    act(() => result.current.resetColumns());
    expect(result.current.visibleColumns.map((c) => c.key)).toEqual(['name', 'floor']);
  });

  it('sorts by the default field, toggles direction on a second click, and restarts ascending on a new field', () => {
    const { result } = renderHook(() =>
      useConfigurableTable({ storageKey: KEY, columns: COLS, defaultSortField: 'name' }),
    );
    expect(result.current.sortRows(ROWS).map((r) => r.name)).toEqual(['Bane', 'Penguin', 'Riddler']);
    act(() => result.current.handleSort('name'));
    expect(result.current.sortDir).toBe('desc');
    expect(result.current.sortRows(ROWS).map((r) => r.name)).toEqual(['Riddler', 'Penguin', 'Bane']);
    act(() => result.current.handleSort('floor'));
    expect(result.current.sortDir).toBe('asc');
    // Numbers compare as numbers: 10 sorts after 3, not before it as text would.
    expect(result.current.sortRows(ROWS).map((r) => r.floor)).toEqual([2, 3, 10]);
  });

  it('setSort sets field and direction outright, and a column without sortValue leaves rows in order', () => {
    const { result } = renderHook(() => useConfigurableTable({ storageKey: KEY, columns: COLS }));
    act(() => result.current.setSort('floor', 'desc'));
    expect(result.current.sortRows(ROWS).map((r) => r.floor)).toEqual([10, 3, 2]);
    act(() => result.current.setSort('notes', 'asc'));
    expect(result.current.sortRows(ROWS)).toBe(ROWS);
  });

  it('resizes a column by the drag distance, never below its minimum', () => {
    const cols: ColumnDef<Row>[] = [{ ...COLS[0], defaultWidth: 100, minWidth: 80 }, COLS[1]];
    const { result } = renderHook(() => useConfigurableTable({ storageKey: KEY, columns: cols }));
    act(() => result.current.startResize('name', 500));
    act(() => {
      document.dispatchEvent(new MouseEvent('mousemove', { clientX: 540 }));
    });
    expect(result.current.columnWidths.name).toBe(140);
    act(() => {
      document.dispatchEvent(new MouseEvent('mousemove', { clientX: 100 }));
    });
    expect(result.current.columnWidths.name).toBe(80);
    act(() => {
      document.dispatchEvent(new MouseEvent('mouseup'));
    });
    // The drag is over: further movement changes nothing.
    act(() => {
      document.dispatchEvent(new MouseEvent('mousemove', { clientX: 900 }));
    });
    expect(result.current.columnWidths.name).toBe(80);
    expect(document.body.style.cursor).toBe('');
  });
});

describe('ResizableHeaderCell', () => {
  const base = { columnKey: 'name', label: 'Name', width: 120, sortActive: false, sortDirection: 'asc' as const };

  it('sorts from the label and starts a resize from the handle', () => {
    const onSort = vi.fn();
    const onStartResize = vi.fn();
    const { container } = render(
      <table><thead><tr><ResizableHeaderCell {...base} onSort={onSort} onStartResize={onStartResize} /></tr></thead></table>,
    );
    fireEvent.click(screen.getByText('Name'));
    expect(onSort).toHaveBeenCalledTimes(1);
    const handle = container.querySelector('th, td')!.lastElementChild!;
    fireEvent.mouseDown(handle, { clientX: 321 });
    expect(onStartResize).toHaveBeenCalledWith('name', 321);
    expect(onSort).toHaveBeenCalledTimes(1);
  });

  it('is a plain label when it is not sortable', () => {
    const { container } = render(
      <table><thead><tr><ResizableHeaderCell {...base} sortable={false} onSort={() => {}} onStartResize={() => {}} /></tr></thead></table>,
    );
    expect(container.querySelector('.MuiTableSortLabel-root')).toBeNull();
    expect(screen.getByText('Name')).toBeInTheDocument();
  });
});

describe('ColumnPicker', () => {
  it('lists the columns with their visibility and toggles and resets through the callbacks', () => {
    const onToggle = vi.fn();
    const onReset = vi.fn();
    render(
      <ColumnPicker
        columns={[{ key: 'name', label: 'Name' }, { key: 'floor', label: 'Floor' }]}
        isVisible={(k) => k === 'name'}
        onToggle={onToggle}
        onReset={onReset}
      />,
    );
    fireEvent.click(screen.getByRole('button'));
    const boxes = screen.getAllByRole('checkbox');
    expect(boxes.map((b) => (b as HTMLInputElement).checked)).toEqual([true, false]);
    fireEvent.click(boxes[1]);
    expect(onToggle).toHaveBeenCalledWith('floor');
    fireEvent.click(screen.getByText('Reset'));
    expect(onReset).toHaveBeenCalledTimes(1);
  });
});
