// file: web/src/components/review/ActionBar.tsx
// version: 1.0.0
// guid: 5a91c73e-2d48-4b06-9f15-8c3e0a7b6d29
// last-edited: 2026-08-20
//
// The bulk-action footer: Apply Selected, Apply High Confidence, Apply Page,
// Skip All Unmatched.
//
// WHY NOT `useOptimistic`
//
// PLAN.md nominates `useOptimistic` here, and it is the wrong tool for this
// particular action -- worth recording, because the reasoning is not obvious and
// the next person will reach for it again.
//
// `useOptimistic` shows a provisional value and reverts it when the surrounding
// action SETTLES. That is exactly right when the action's promise resolving
// means the work is done. Here it does not: `batchApplyFromCache` dispatches a
// background operation and returns an op id in well under a second, while the
// apply itself runs for minutes. Wiring `useOptimistic` to that promise would
// revert every row to "pending" seconds after the click, while the server was
// still working -- the rows would flicker back and then flip forward again on
// the next refresh, and a reviewer watching that would reasonably conclude the
// apply had failed.
//
// So the optimistic update lives in the lane hook's `rowStates`, where it
// persists until the server's own answer replaces it, and this component uses
// `useTransition` for the part that IS bounded: keeping the button disabled and
// showing a spinner while the dispatch request is in flight. That is the Actions
// pattern doing the job it is actually suited to.
//
// The vocabulary comes from the lane descriptor rather than from string literals
// here, so the dedup lane's "Dismiss" and the metadata lane's "Reject match"
// stay distinguishable. `verbs` is a total map over the lane's own action types,
// so a lane that gains an action without naming it fails to compile.

import { useTransition } from 'react';
import { Box, Button, CircularProgress, Stack, Tooltip, Typography } from '@mui/material';
import type { MetadataAction } from './reviewActions';
import { needsConfirmation } from './reviewActions';
import { metadataLane } from './lanes';

export interface ActionBarProps {
  selectedIds: Set<string>;
  highConfidenceIds: string[];
  allVisiblePendingIds: string[];
  unmatchedCount: number;
  applying: boolean;
  dispatch: (action: MetadataAction) => void;
  /**
   * Asks the reviewer to confirm a destructive or wide-reaching action.
   * Injected rather than called directly so the bar stays testable without a
   * dialog host, and so a future confirm UI does not mean editing this file.
   */
  confirm: (message: string) => Promise<boolean>;
}

export function ActionBar({
  selectedIds,
  highConfidenceIds,
  allVisiblePendingIds,
  unmatchedCount,
  applying,
  dispatch,
  confirm,
}: ActionBarProps) {
  const [pending, startTransition] = useTransition();
  const verbs = metadataLane.verbs;
  const busy = pending || applying;

  const run = (action: MetadataAction, count: number) => {
    startTransition(async () => {
      if (needsConfirmation(action)) {
        const ok = await confirm(`Apply metadata to ${count.toLocaleString()} book(s)?`);
        if (!ok) return;
      }
      dispatch(action);
    });
  };

  const selected = [...selectedIds];

  return (
    <Box
      data-testid="action-bar"
      component="footer"
      sx={{
        display: 'flex',
        alignItems: 'center',
        gap: 1,
        px: 2,
        py: 1,
        borderTop: 1,
        borderColor: 'divider',
        bgcolor: 'background.paper',
        flexWrap: 'wrap',
      }}
    >
      <Typography variant="body2" color="text.secondary" sx={{ mr: 'auto' }}>
        {selected.length > 0 ? `${selected.length} selected` : 'Nothing selected'}
      </Typography>

      {busy && <CircularProgress size={18} aria-label="Applying" />}

      <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: 'wrap' }}>
        <Tooltip title="Skip every row the providers could not match. Skipped rows stay actionable.">
          <span>
            <Button
              size="small"
              disabled={busy || unmatchedCount === 0}
              data-testid="skip-all-unmatched"
              onClick={() => dispatch({ lane: 'metadata', type: 'skipAllUnmatched' })}
            >
              {verbs.skipAllUnmatched} ({unmatchedCount})
            </Button>
          </span>
        </Tooltip>

        <Tooltip title="Rows on this page scoring above the confidence threshold that also name a narrator.">
          <span>
            <Button
              size="small"
              variant="outlined"
              disabled={busy || highConfidenceIds.length === 0}
              data-testid="apply-high-confidence"
              onClick={() =>
                run(
                  { lane: 'metadata', type: 'applySelected', ids: highConfidenceIds },
                  highConfidenceIds.length
                )
              }
            >
              Apply high confidence ({highConfidenceIds.length})
            </Button>
          </span>
        </Tooltip>

        <Tooltip title="Every undecided matched row on this page.">
          <span>
            <Button
              size="small"
              variant="outlined"
              disabled={busy || allVisiblePendingIds.length === 0}
              data-testid="apply-page"
              onClick={() =>
                run(
                  { lane: 'metadata', type: 'applySelected', ids: allVisiblePendingIds },
                  allVisiblePendingIds.length
                )
              }
            >
              Apply page ({allVisiblePendingIds.length})
            </Button>
          </span>
        </Tooltip>

        <Button
          size="small"
          variant="contained"
          disabled={busy || selected.length === 0}
          data-testid="apply-selected"
          onClick={() =>
            run({ lane: 'metadata', type: 'applySelected', ids: selected }, selected.length)
          }
        >
          {verbs.applySelected} ({selected.length})
        </Button>
      </Stack>
    </Box>
  );
}
