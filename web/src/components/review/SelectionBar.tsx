// file: web/src/components/review/SelectionBar.tsx
// version: 1.0.0
// guid: 01e0fbea-db00-4998-908b-48f24aad9a5d
// last-edited: 2026-10-06
//
// The metadata lane's selection controls, ALWAYS rendered.
//
// Select-all used to be two-stage and conditional: a bare checkbox in the queue
// header that only existed when the page had rows, and a "Select all N
// matching" banner that only appeared after that checkbox had ticked the whole
// page. The owner could not find select-all at all. This bar is on screen in
// every view mode, with or without a chip, even at zero rows -- the buttons go
// disabled rather than disappearing, so the controls are always where the eye
// expects them.
//
// It renders twice (sticky over the spine, and in the rail's queue header), so
// every test id is prefixed by `testIdPrefix`. The rail instance uses '' and
// keeps the ids the old checkbox and banner had (`select-page`,
// `select-all-matching`). Neither instance uses `selected-count` or
// `clear-selection`: ActionBar owns those.

import { Box, Button, Typography } from '@mui/material';
import type { SxProps, Theme } from '@mui/material';

export interface SelectionBarProps {
  /** Book ids on the current page (every row, grouped or not). */
  pageIds: string[];
  /** How many books the current view matches across all pages. */
  matchingCount: number;
  selectedCount: number;
  /** True when every book the current view matches is selected. */
  allMatchingSelected: boolean;
  isSelected: (id: string) => boolean;
  onSelectPage: (ids: string[], selected: boolean) => void;
  onSelectAllMatching: () => void;
  onClearSelection: () => void;
  /** Prefix for every test id; '' keeps the rail's legacy ids. */
  testIdPrefix?: string;
  /** Disable everything while the lane loads. */
  disabled?: boolean;
  sx?: SxProps<Theme>;
}

export function SelectionBar({
  pageIds,
  matchingCount,
  selectedCount,
  allMatchingSelected,
  isSelected,
  onSelectPage,
  onSelectAllMatching,
  onClearSelection,
  testIdPrefix = '',
  disabled = false,
  sx,
}: SelectionBarProps) {
  const id = (name: string) => `${testIdPrefix}${name}`;
  const pageCount = pageIds.length;
  // A toggle, as the checkbox it replaces was: when the whole page is already
  // ticked, the same button unticks it.
  const pageAllSelected = pageCount > 0 && pageIds.every((pid) => isSelected(pid));

  return (
    <Box
      data-testid={id('selection-bar')}
      role="toolbar"
      aria-label="Selection"
      sx={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 0.5, ...sx }}
    >
      <Button
        size="small"
        variant="outlined"
        data-testid={id('select-page')}
        aria-pressed={pageAllSelected}
        disabled={disabled || pageCount === 0}
        onClick={() => onSelectPage(pageIds, !pageAllSelected)}
      >
        {pageAllSelected ? 'Deselect page' : 'Select page'} ({pageCount.toLocaleString()})
      </Button>
      <Button
        size="small"
        variant="outlined"
        data-testid={id('select-all-matching')}
        disabled={disabled || matchingCount === 0 || allMatchingSelected}
        onClick={onSelectAllMatching}
      >
        Select all {matchingCount.toLocaleString()} matching
      </Button>
      <Button
        size="small"
        data-testid={id('selection-clear')}
        disabled={selectedCount === 0}
        onClick={onClearSelection}
      >
        Clear
      </Button>
      <Typography
        variant="caption"
        color="text.secondary"
        data-testid={id('selection-count')}
        aria-live="polite"
        sx={{ ml: 0.5 }}
      >
        {selectedCount.toLocaleString()} selected
        {allMatchingSelected ? ` (all ${matchingCount.toLocaleString()} matching)` : ''}
      </Typography>
    </Box>
  );
}
