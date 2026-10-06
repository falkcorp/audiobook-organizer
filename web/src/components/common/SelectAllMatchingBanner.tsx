// file: web/src/components/common/SelectAllMatchingBanner.tsx
// version: 1.1.0
// guid: d1de8a62-0999-4e49-b0cd-906d5ce65e22
// last-edited: 2026-10-06

/**
 * The Gmail-pattern banner that goes with useRowSelection:
 *
 *   page fully selected  -> "All N on this page are selected. Select all M matching"
 *   all matching         -> "All M selected. Clear selection"
 *
 * Renders nothing in any other state. `unavailableReason`, when set, replaces
 * the "Select all M" link with the reason, so a reviewer who selected the page
 * learns WHY the wider selection is not offered instead of seeing nothing.
 *
 * `countState` says how far `totalMatching` can be trusted when the caller
 * counts the matching rows separately from the list (the dedup lanes ask the
 * server's bulk count): "counting" shows no number yet, "approximate" marks
 * the list's own total, shown because the count failed.
 */

import { Alert, Button } from '@mui/material';
import type { RowSelection } from '../../hooks/useRowSelection';

export interface SelectAllMatchingBannerProps<K> {
  selection: RowSelection<K>;
  /** Selectable rows on this page. */
  pageCount: number;
  totalMatching: number;
  /** Plural noun for the rows, e.g. "candidates", "groups". */
  noun?: string;
  /** Why "select all matching" is not offered, when it is not. */
  unavailableReason?: string | null;
  testIdPrefix?: string;
  /** Omitted = `totalMatching` is exact. */
  countState?: 'counting' | 'approximate';
}

export function SelectAllMatchingBanner<K>({
  selection,
  pageCount,
  totalMatching,
  noun = 'rows',
  unavailableReason = null,
  testIdPrefix = 'select-all',
  countState,
}: SelectAllMatchingBannerProps<K>) {
  const counting = countState === 'counting';
  const n = `${countState === 'approximate' ? 'about ' : ''}${totalMatching.toLocaleString()}`;
  if (selection.allMatching) {
    return (
      <Alert
        severity="info"
        data-testid={`${testIdPrefix}-banner`}
        sx={{ my: 1 }}
        action={
          <Button size="small" onClick={selection.clear} data-testid={`${testIdPrefix}-clear`}>
            Clear selection
          </Button>
        }
      >
        {counting
          ? `All ${noun} matching this filter are selected (counting…).`
          : `All ${n} ${noun} matching this filter are selected.`}
      </Alert>
    );
  }
  if (!selection.pageFullySelected || totalMatching <= pageCount) return null;
  if (!selection.showSelectAllMatching && !unavailableReason) return null;
  return (
    <Alert
      severity="info"
      data-testid={`${testIdPrefix}-banner`}
      sx={{ my: 1 }}
      action={
        selection.showSelectAllMatching ? (
          <Button
            size="small"
            onClick={selection.selectAllMatching}
            data-testid={`${testIdPrefix}-matching`}
          >
            {counting ? 'Select all matching (counting…)' : `Select all ${n} matching`}
          </Button>
        ) : undefined
      }
    >
      All {pageCount.toLocaleString()} {noun} on this page are selected.
      {!selection.showSelectAllMatching && unavailableReason ? ` ${unavailableReason}` : ''}
    </Alert>
  );
}
