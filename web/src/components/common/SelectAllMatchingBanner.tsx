// file: web/src/components/common/SelectAllMatchingBanner.tsx
// version: 1.0.0
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
}

export function SelectAllMatchingBanner<K>({
  selection,
  pageCount,
  totalMatching,
  noun = 'rows',
  unavailableReason = null,
  testIdPrefix = 'select-all',
}: SelectAllMatchingBannerProps<K>) {
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
        All {totalMatching.toLocaleString()} {noun} matching this filter are selected.
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
            Select all {totalMatching.toLocaleString()} matching
          </Button>
        ) : undefined
      }
    >
      All {pageCount.toLocaleString()} {noun} on this page are selected.
      {!selection.showSelectAllMatching && unavailableReason ? ` ${unavailableReason}` : ''}
    </Alert>
  );
}
