// file: core/web/packages/ui-kit/src/hooks/index.ts
// version: 0.1.0
// guid: 7f943bca-2a8a-4381-87be-b0142c0526ad
// last-edited: 2026-11-21

// ui-kit's hooks (07 §7.11). useKeyboardShortcuts lives under shortcuts/ (F9-01).

export { useAsyncAction, type AsyncActionState, type UseAsyncActionReturn } from './useAsyncAction';
export { serverAnsweredTerm, useDebouncedSearch, useSettledSearch } from './useDebouncedSearch';
export {
  applyRowClick,
  useRowSelection,
  type RowCheckboxProps,
  type RowSelection,
  type SelectionMode,
  type UseRowSelectionOptions,
} from './useRowSelection';
