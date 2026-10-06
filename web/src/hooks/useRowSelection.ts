// file: web/src/hooks/useRowSelection.ts
// version: 1.0.0
// guid: d82ff76a-e17f-48bf-9c08-4779955f6877
// last-edited: 2026-10-06

/**
 * useRowSelection: the selection model for a paged list with per-row
 * checkboxes and bulk actions. Owner request 2026-10-06 for the dedup review
 * surfaces: "select all which selects all on the page, and then select all
 * across all pages, it also needs to support shift to select multiple".
 *
 * Three behaviours, one model:
 *
 *  1. A header checkbox selects every selectable row on the CURRENT page
 *     (indeterminate when some are; unchecking it clears the selection).
 *  2. Once the page is fully selected and more rows match than the page
 *     shows, `showSelectAllMatching` is true and `selectAllMatching()` moves
 *     the selection into "every row matching the current filter" (Gmail's
 *     pattern). In that mode `selectedCount` is `totalMatching`, and the
 *     caller must act on the FILTER, not on `selected` -- unless it passed
 *     `allKeys`, in which case the full key list is materialised into
 *     `selected` and the caller can keep acting on ids.
 *  3. Shift-click (and Shift+Space on a focused checkbox) sets every row
 *     between the last-clicked row and this one to the anchor's state: a
 *     range of checks when the anchor is checked, a range of unchecks when it
 *     is not. This is the file-manager convention, and it is deliberately
 *     NOT the add-only behaviour of applyFieldClick / the old dupes-lane
 *     toggle, because the request is to select OR deselect a run.
 *
 * `resetKey` is the caller's filter + page-size identity. When it changes the
 * whole selection is dropped DURING RENDER (React's "adjust state when a prop
 * changes" pattern), not in an effect: an effect leaves one committed render
 * in which "all matching" still refers to the previous filter and a bulk
 * action could be dispatched against it.
 *
 * The anchor is stored as a KEY, not an index. An index means nothing after a
 * page turn (the same index is a different row), whereas a key that is no
 * longer on the page simply fails to resolve and the click degrades to a
 * plain toggle.
 */

import { useCallback, useMemo, useRef, useState } from 'react';
import type React from 'react';

export type SelectionMode = 'ids' | 'allMatching';

export interface UseRowSelectionOptions<K> {
  /** Keys of the rows on the current page, in display order. */
  pageKeys: readonly K[];
  /** How many rows match the current filter across every page. */
  totalMatching: number;
  /** Filter + page-size identity. A change clears the whole selection. */
  resetKey: string;
  /** Rows that cannot be selected (already applied, busy...). */
  isDisabled?: (key: K) => boolean;
  /**
   * The full matching key list, when the caller already holds it (client-side
   * paging). With it, "select all matching" materialises every key into
   * `selected`, so bulk actions keep working on ids.
   */
  allKeys?: readonly K[];
  /**
   * False when the caller cannot act on "every row matching this filter" (the
   * bulk endpoint cannot express the filter, a search is still settling...).
   * Hides the banner's "Select all M" and drops an active all-matching
   * selection back to nothing.
   */
  canSelectAllMatching?: boolean;
}

export interface RowCheckboxProps {
  checked: boolean;
  disabled: boolean;
  onChange: (e: React.ChangeEvent<HTMLInputElement>) => void;
  onKeyDown: (e: React.KeyboardEvent<HTMLElement>) => void;
}

export interface RowSelection<K> {
  /** Explicit selection. Empty in all-matching mode unless `allKeys` was given. */
  selected: ReadonlySet<K>;
  mode: SelectionMode;
  allMatching: boolean;
  /** `totalMatching` in all-matching mode, otherwise `selected.size`. */
  selectedCount: number;
  isSelected: (key: K) => boolean;
  /** State for the header checkbox. */
  header: { checked: boolean; indeterminate: boolean; disabled: boolean };
  /** Header checkbox click: select the page, or clear when it is already fully selected. */
  togglePage: () => void;
  /** Row click. With `shiftKey`, applies the anchor's state to the range. */
  toggle: (key: K, shiftKey?: boolean) => void;
  selectAllMatching: () => void;
  clear: () => void;
  /** Replace the explicit selection (e.g. keep only the rows whose action failed). */
  replace: (keys: Iterable<K>) => void;
  /** Every selectable row on this page is selected. */
  pageFullySelected: boolean;
  /** Show the banner's "Select all M matching" link. */
  showSelectAllMatching: boolean;
  /** Props for a row's checkbox: change handler that reads Shift, and Shift+Space. */
  checkboxProps: (key: K) => RowCheckboxProps;
}

/**
 * The pure core of a row click, exported for tests. Returns the next explicit
 * selection; `anchor` is the previous anchor key (or null).
 *
 * A shift-click with a resolvable anchor sets every selectable row in
 * [anchor, key] to the anchor's CURRENT state in `prev`. Anything else
 * toggles `key` alone.
 */
export function applyRowClick<K>(
  prev: ReadonlySet<K>,
  key: K,
  shiftKey: boolean,
  anchor: K | null,
  pageKeys: readonly K[],
  isDisabled: (key: K) => boolean = () => false
): Set<K> {
  const next = new Set(prev);
  const ki = pageKeys.indexOf(key);
  const ai = anchor === null ? -1 : pageKeys.indexOf(anchor);
  if (shiftKey && ki >= 0 && ai >= 0 && ai !== ki) {
    const target = prev.has(anchor as K);
    const [lo, hi] = ai < ki ? [ai, ki] : [ki, ai];
    for (let i = lo; i <= hi; i++) {
      const k = pageKeys[i];
      if (isDisabled(k)) continue;
      if (target) next.add(k);
      else next.delete(k);
    }
    return next;
  }
  if (isDisabled(key)) return next;
  if (next.has(key)) next.delete(key);
  else next.add(key);
  return next;
}

const NEVER_DISABLED = () => false;

export function useRowSelection<K>({
  pageKeys,
  totalMatching,
  resetKey,
  isDisabled = NEVER_DISABLED,
  allKeys,
  canSelectAllMatching = true,
}: UseRowSelectionOptions<K>): RowSelection<K> {
  const [selected, setSelected] = useState<Set<K>>(() => new Set());
  const [allMatchingState, setAllMatching] = useState(false);
  // State, not a ref: it is reset during render on a filter change, and a ref
  // write during render is unsafe under concurrent rendering.
  const [anchor, setAnchor] = useState<K | null>(null);
  // Set by Shift+Space on a focused checkbox, read by the change event that
  // the keyboard activation fires next. Whether a keyboard-activated click
  // carries shiftKey differs by browser, so it is not trusted.
  const shiftSpaceRef = useRef(false);

  // Filter / page-size change: drop everything, during render.
  const [prevResetKey, setPrevResetKey] = useState(resetKey);
  if (prevResetKey !== resetKey) {
    setPrevResetKey(resetKey);
    setSelected(new Set());
    setAllMatching(false);
    setAnchor(null);
  }

  // An all-matching selection the caller can no longer act on is dropped on
  // READ rather than kept armed: the banner would otherwise promise a set the
  // bulk action refuses.
  const allMatching = allMatchingState && canSelectAllMatching;

  const selectable = useMemo(() => pageKeys.filter((k) => !isDisabled(k)), [pageKeys, isDisabled]);

  // In all-matching mode without a local key list, every selectable row is
  // selected by definition; `selected` is not consulted.
  const virtualAll = allMatching && !allKeys;

  const isSelected = useCallback(
    (key: K) => (virtualAll ? !isDisabled(key) : selected.has(key)),
    [virtualAll, isDisabled, selected]
  );

  const selectedOnPage = useMemo(
    () => selectable.reduce((n, k) => (isSelected(k) ? n + 1 : n), 0),
    [selectable, isSelected]
  );
  const pageFullySelected = selectable.length > 0 && selectedOnPage === selectable.length;

  const selectedCount = virtualAll ? totalMatching : selected.size;

  const everythingSelected =
    allMatching ||
    (allKeys !== undefined && allKeys.length > 0 && allKeys.every((k) => selected.has(k)));

  const showSelectAllMatching =
    canSelectAllMatching &&
    pageFullySelected &&
    !everythingSelected &&
    totalMatching > selectable.length;

  /** The explicit set a row click starts from: the page itself when in virtual all-matching mode. */
  const baseSet = useCallback(
    (): Set<K> => (virtualAll ? new Set(selectable) : new Set(selected)),
    [virtualAll, selectable, selected]
  );

  const toggle = useCallback(
    (key: K, shiftKey = false) => {
      const base = baseSet();
      const next = applyRowClick(base, key, shiftKey, anchor, pageKeys, isDisabled);
      setAnchor(key);
      // Any manual change leaves all-matching mode: the selection is now an
      // explicit list again, and saying "all M selected" would be false.
      setAllMatching(false);
      setSelected(next);
    },
    [baseSet, anchor, pageKeys, isDisabled]
  );

  const clear = useCallback(() => {
    setSelected(new Set());
    setAllMatching(false);
    setAnchor(null);
  }, []);

  const togglePage = useCallback(() => {
    if (pageFullySelected) {
      clear();
      return;
    }
    setAllMatching(false);
    setSelected((prev) => {
      const next = new Set(prev);
      for (const k of selectable) next.add(k);
      return next;
    });
  }, [pageFullySelected, clear, selectable]);

  const selectAllMatching = useCallback(() => {
    if (!canSelectAllMatching) return;
    setAllMatching(true);
    if (allKeys) setSelected(new Set(allKeys.filter((k) => !isDisabled(k))));
    else setSelected(new Set());
  }, [canSelectAllMatching, allKeys, isDisabled]);

  const replace = useCallback((keys: Iterable<K>) => {
    setAllMatching(false);
    setSelected(new Set(keys));
  }, []);

  const checkboxProps = useCallback(
    (key: K): RowCheckboxProps => ({
      checked: isSelected(key),
      disabled: isDisabled(key),
      onChange: (e) => {
        const native = e.nativeEvent as Partial<MouseEvent>;
        const shift = Boolean(native.shiftKey) || shiftSpaceRef.current;
        shiftSpaceRef.current = false;
        toggle(key, shift);
      },
      onKeyDown: (e) => {
        shiftSpaceRef.current = e.key === ' ' && e.shiftKey;
      },
    }),
    [isSelected, isDisabled, toggle]
  );

  return {
    selected,
    mode: allMatching ? 'allMatching' : 'ids',
    allMatching,
    selectedCount,
    isSelected,
    header: {
      checked: pageFullySelected,
      indeterminate: selectedOnPage > 0 && !pageFullySelected,
      disabled: selectable.length === 0,
    },
    togglePage,
    toggle,
    selectAllMatching,
    clear,
    replace,
    pageFullySelected,
    showSelectAllMatching,
    checkboxProps,
  };
}
