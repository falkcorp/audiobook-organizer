// file: web/src/components/review/AuthorsSubViewSwitch.tsx
// version: 1.1.0
// guid: 4a90d6e3-7b15-4c82-9f0a-d38e12b6c574
// last-edited: 2026-10-13

/**
 * The Authors & series lane's sub-view control: Authors | Series | AI scans. Every
 * sub-view panel renders it at the top of its rail, so the lane keeps one control in
 * one place whichever sub-view is showing.
 *
 * Choosing a sub-view is local state in the lane hook. It never writes `?view=`:
 * the URL is how you ARRIVE at a sub-view, not a running record of the one you
 * are on (the same rule as `?lane=`).
 */

import { ToggleButton, ToggleButtonGroup } from '@mui/material';
import { authorsLane } from './lanes/authors';
import type { AuthorsSubView } from './lanes/useAuthorsLane';

const SUB_VIEWS: ReadonlyArray<{ value: AuthorsSubView; label: string }> = [
  { value: 'authors', label: 'Authors' },
  { value: 'series', label: 'Series' },
  { value: 'ai', label: 'AI scans' },
];

export function AuthorsSubViewSwitch({
  view,
  onChange,
}: {
  view: AuthorsSubView;
  onChange: (view: AuthorsSubView) => void;
}) {
  return (
    <ToggleButtonGroup
      size="small"
      exclusive
      value={view}
      // Clicking the selected button yields null; the control always has one.
      onChange={(_, v: AuthorsSubView | null) => v && onChange(v)}
      aria-label={`${authorsLane.label} sub-view`}
    >
      {SUB_VIEWS.map(({ value, label }) => (
        <ToggleButton key={value} value={value} data-testid={`authors-subview-${value}`}>
          {label}
        </ToggleButton>
      ))}
    </ToggleButtonGroup>
  );
}
