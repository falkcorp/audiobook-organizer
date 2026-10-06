// file: web/src/hooks/useRowSelection.test.ts
// version: 1.0.0
// guid: 74512c01-e935-4c9e-bc5f-de7092c46a0c
// last-edited: 2026-10-06

import type React from 'react';
import { act, renderHook } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { applyRowClick, useRowSelection, type UseRowSelectionOptions } from './useRowSelection';

const PAGE = ['a', 'b', 'c', 'd', 'e'];

function setup(initial: Partial<UseRowSelectionOptions<string>> = {}) {
  const props: UseRowSelectionOptions<string> = {
    pageKeys: PAGE,
    totalMatching: 23,
    resetKey: 'f1',
    ...initial,
  };
  return renderHook((p: UseRowSelectionOptions<string>) => useRowSelection(p), {
    initialProps: props,
  });
}

const sorted = (s: ReadonlySet<string>) => [...s].sort();

describe('applyRowClick', () => {
  it('toggles a single key without shift', () => {
    expect(sorted(applyRowClick(new Set<string>(), 'b', false, null, PAGE))).toEqual(['b']);
    expect(sorted(applyRowClick(new Set(['b']), 'b', false, null, PAGE))).toEqual([]);
  });

  it('shift with no anchor on the page is a plain toggle', () => {
    expect(sorted(applyRowClick(new Set<string>(), 'c', true, 'zz', PAGE))).toEqual(['c']);
  });
});

describe('useRowSelection', () => {
  it('header checkbox selects the page, is indeterminate when partial, and unchecks to clear', () => {
    const { result } = setup();
    expect(result.current.header).toEqual({
      checked: false,
      indeterminate: false,
      disabled: false,
    });

    act(() => result.current.toggle('b'));
    expect(result.current.header.indeterminate).toBe(true);
    expect(result.current.header.checked).toBe(false);

    act(() => result.current.togglePage());
    expect(sorted(result.current.selected)).toEqual(PAGE);
    expect(result.current.header).toMatchObject({ checked: true, indeterminate: false });
    expect(result.current.selectedCount).toBe(5);

    act(() => result.current.togglePage());
    expect(result.current.selectedCount).toBe(0);
    expect(result.current.header).toMatchObject({ checked: false, indeterminate: false });
  });

  it('offers select-all-matching only once the page is full and more rows match', () => {
    const { result, rerender } = setup();
    expect(result.current.showSelectAllMatching).toBe(false);
    act(() => result.current.togglePage());
    expect(result.current.showSelectAllMatching).toBe(true);

    // Everything fits on one page: nothing more to offer.
    rerender({ pageKeys: PAGE, totalMatching: 5, resetKey: 'f1' });
    expect(result.current.showSelectAllMatching).toBe(false);
  });

  it('cross-page: selectAllMatching counts every matching row and survives a page turn', () => {
    const { result, rerender } = setup();
    act(() => result.current.togglePage());
    act(() => result.current.selectAllMatching());
    expect(result.current.allMatching).toBe(true);
    expect(result.current.mode).toBe('allMatching');
    expect(result.current.selectedCount).toBe(23);
    expect(result.current.isSelected('e')).toBe(true);

    // Next page, same filter: still all matching, and its rows read selected.
    rerender({ pageKeys: ['f', 'g', 'h'], totalMatching: 23, resetKey: 'f1' });
    expect(result.current.allMatching).toBe(true);
    expect(result.current.isSelected('g')).toBe(true);
    expect(result.current.header.checked).toBe(true);
  });

  it('cross-page: unticking one row drops to an explicit selection of the page minus it', () => {
    const { result } = setup();
    act(() => result.current.togglePage());
    act(() => result.current.selectAllMatching());
    act(() => result.current.toggle('c'));
    expect(result.current.allMatching).toBe(false);
    expect(sorted(result.current.selected)).toEqual(['a', 'b', 'd', 'e']);
    expect(result.current.selectedCount).toBe(4);
  });

  it('with allKeys, selectAllMatching materialises every key so callers keep acting on ids', () => {
    const all = Array.from({ length: 12 }, (_, i) => `k${i}`);
    const { result } = setup({ pageKeys: all.slice(0, 5), totalMatching: 12, allKeys: all });
    act(() => result.current.togglePage());
    expect(result.current.showSelectAllMatching).toBe(true);
    act(() => result.current.selectAllMatching());
    expect(result.current.selected.size).toBe(12);
    expect(result.current.selectedCount).toBe(12);
    expect(result.current.showSelectAllMatching).toBe(false);
  });

  it('filter change clears the cross-page selection and the explicit one', () => {
    const { result, rerender } = setup();
    act(() => result.current.togglePage());
    act(() => result.current.selectAllMatching());
    rerender({ pageKeys: PAGE, totalMatching: 9, resetKey: 'f2' });
    expect(result.current.allMatching).toBe(false);
    expect(result.current.selectedCount).toBe(0);

    act(() => result.current.toggle('a'));
    rerender({ pageKeys: PAGE, totalMatching: 9, resetKey: 'f3-pagesize' });
    expect(result.current.selectedCount).toBe(0);
  });

  it('canSelectAllMatching=false hides the offer and drops an armed all-matching selection', () => {
    const { result, rerender } = setup();
    act(() => result.current.togglePage());
    act(() => result.current.selectAllMatching());
    rerender({ pageKeys: PAGE, totalMatching: 23, resetKey: 'f1', canSelectAllMatching: false });
    expect(result.current.allMatching).toBe(false);
    expect(result.current.showSelectAllMatching).toBe(false);
  });

  it('shift range forward selects every row between the anchor and the click', () => {
    const { result } = setup();
    act(() => result.current.toggle('b'));
    act(() => result.current.toggle('e', true));
    expect(sorted(result.current.selected)).toEqual(['b', 'c', 'd', 'e']);
  });

  it('shift range backward selects the same span', () => {
    const { result } = setup();
    act(() => result.current.toggle('d'));
    act(() => result.current.toggle('a', true));
    expect(sorted(result.current.selected)).toEqual(['a', 'b', 'c', 'd']);
  });

  it('shift range deselects when the anchor was just unchecked', () => {
    const { result } = setup();
    act(() => result.current.togglePage());
    act(() => result.current.toggle('b')); // anchor b, now unchecked
    act(() => result.current.toggle('d', true));
    expect(sorted(result.current.selected)).toEqual(['a', 'e']);
  });

  it('shift range skips disabled rows', () => {
    const { result } = setup({ isDisabled: (k) => k === 'c' });
    act(() => result.current.toggle('a'));
    act(() => result.current.toggle('e', true));
    expect(sorted(result.current.selected)).toEqual(['a', 'b', 'd', 'e']);
    // And the header treats the page as full without the disabled row.
    expect(result.current.header.checked).toBe(true);
  });

  it('a stale anchor from another page degrades to a plain toggle', () => {
    const { result, rerender } = setup();
    act(() => result.current.toggle('b'));
    rerender({ pageKeys: ['f', 'g', 'h'], totalMatching: 23, resetKey: 'f1' });
    act(() => result.current.toggle('h', true));
    expect(sorted(result.current.selected)).toEqual(['b', 'h']);
  });

  it('checkboxProps: Shift+Space on a focused checkbox extends the range', () => {
    const { result } = setup();
    act(() => result.current.toggle('a'));
    const props = result.current.checkboxProps('c');
    act(() => {
      props.onKeyDown({ key: ' ', shiftKey: true } as React.KeyboardEvent<HTMLElement>);
      // The keyboard activation's change event carries no shiftKey.
      props.onChange({ nativeEvent: {} } as React.ChangeEvent<HTMLInputElement>);
    });
    expect(sorted(result.current.selected)).toEqual(['a', 'b', 'c']);
  });

  it('checkboxProps: a shift-click change event extends the range', () => {
    const { result } = setup();
    act(() => result.current.toggle('e'));
    const props = result.current.checkboxProps('c');
    act(() =>
      props.onChange({
        nativeEvent: { shiftKey: true },
      } as unknown as React.ChangeEvent<HTMLInputElement>)
    );
    expect(sorted(result.current.selected)).toEqual(['c', 'd', 'e']);
  });
});
