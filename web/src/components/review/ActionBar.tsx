// file: web/src/components/review/ActionBar.tsx
// version: 1.4.0
// guid: 5a91c73e-2d48-4b06-9f15-8c3e0a7b6d29
// last-edited: 2026-09-27
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
import {
  Box,
  Button,
  CircularProgress,
  Stack,
  ToggleButton,
  ToggleButtonGroup,
  Tooltip,
  Typography,
} from '@mui/material';
import type { BulkApplyMode } from '../../services/api';
import type { MetadataAction } from './reviewActions';
import { needsConfirmation } from './reviewActions';
import { metadataLane } from './lanes';

export interface ActionBarProps {
  selectedIds: Set<string>;
  /**
   * The part of the selection Apply selected can act on: the selection minus
   * books known to have no candidate. Defaults to the whole selection, which
   * is what it is whenever no candidate-less row is selected.
   */
  applicableSelectedIds?: string[];
  /**
   * Search again for every selected book with its own title and author. When
   * absent the button is not rendered.
   */
  onSearchSelected?: (ids: string[]) => void;
  /** A Search again request is in flight. */
  searching?: boolean;
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
  /**
   * The bulk buttons' toggle (owner ruling 2026-09-27): 'fill' writes only
   * empty fields, 'replace' overwrites filled ones. It governs the three bulk
   * buttons here (and the lane's other applySelected entry points); a
   * single-row Apply is unaffected.
   */
  bulkApplyMode: BulkApplyMode;
  onBulkApplyModeChange: (mode: BulkApplyMode) => void;
  /**
   * True once the owner ticked "Don't ask me again" on the Replace prompt.
   * While set, a small "Ask before replacing again" control sits next to the
   * toggle so the choice can be undone without clearing browser storage.
   */
  replaceConfirmSkipped?: boolean;
  onResetReplaceConfirm?: () => void;
}

export function ActionBar({
  selectedIds,
  applicableSelectedIds,
  onSearchSelected,
  searching = false,
  highConfidenceIds,
  allVisiblePendingIds,
  unmatchedCount,
  applying,
  dispatch,
  confirm,
  bulkApplyMode,
  onBulkApplyModeChange,
  replaceConfirmSkipped = false,
  onResetReplaceConfirm,
}: ActionBarProps) {
  const [pending, startTransition] = useTransition();
  const verbs = metadataLane.verbs;
  const busy = pending || applying;
  const replacing = bulkApplyMode === 'replace';
  // Suffix on every bulk button's label while the toggle is on Replace, so the
  // button that overwrites says so itself.
  const modeSuffix = replacing ? ', replace existing' : '';

  const run = (action: MetadataAction, count: number) => {
    startTransition(async () => {
      // Replace mode is confirmed by the lane's applySelected dispatch
      // (useMetadataLane), once for every bulk entry point, so it is not
      // asked again here.
      if (needsConfirmation(action)) {
        const ok = await confirm(`Apply metadata to ${count.toLocaleString()} book(s)?`);
        if (!ok) return;
      }
      dispatch(action);
    });
  };

  const selected = [...selectedIds];
  const applicable = applicableSelectedIds ?? selected;

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

      <Tooltip title="What the bulk Apply buttons do to fields a book already has. Fill leaves them alone; Replace overwrites them. A single-row Apply always replaces.">
        <ToggleButtonGroup
          size="small"
          exclusive
          value={bulkApplyMode}
          disabled={busy}
          aria-label="Bulk apply mode"
          data-testid="bulk-apply-mode"
          onChange={(_, next: BulkApplyMode | null) => {
            // Exclusive groups report null when the active button is
            // clicked again; keep the current mode rather than clearing it.
            if (next) onBulkApplyModeChange(next);
          }}
        >
          <ToggleButton value="fill" data-testid="bulk-apply-mode-fill">
            Fill empty fields
          </ToggleButton>
          <ToggleButton value="replace" color="warning" data-testid="bulk-apply-mode-replace">
            Replace existing
          </ToggleButton>
        </ToggleButtonGroup>
      </Tooltip>

      {replaceConfirmSkipped && onResetReplaceConfirm && (
        <Tooltip title="You chose not to be asked before a Replace bulk apply. Click to get the prompt back.">
          <Button
            size="small"
            variant="text"
            color="inherit"
            data-testid="reset-replace-confirm"
            sx={{ textTransform: 'none', color: 'text.secondary' }}
            onClick={onResetReplaceConfirm}
          >
            Ask before replacing again
          </Button>
        </Tooltip>
      )}

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
              Apply high confidence{modeSuffix} ({highConfidenceIds.length})
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
              Apply page{modeSuffix} ({allVisiblePendingIds.length})
            </Button>
          </span>
        </Tooltip>

        {onSearchSelected && (
          <Tooltip title="Ask the providers again for every selected book, using each book's current title and author. Runs as one background search (watch the bell); the new candidates appear here to review when it finishes.">
            <span>
              <Button
                size="small"
                variant="outlined"
                disabled={searching || selected.length === 0}
                data-testid="search-selected"
                onClick={() => onSearchSelected(selected)}
              >
                Search again ({selected.length})
              </Button>
            </span>
          </Tooltip>
        )}

        <Button
          size="small"
          variant="contained"
          disabled={busy || applicable.length === 0}
          data-testid="apply-selected"
          onClick={() =>
            run({ lane: 'metadata', type: 'applySelected', ids: applicable }, applicable.length)
          }
        >
          {verbs.applySelected}
          {modeSuffix} ({applicable.length})
        </Button>
      </Stack>
    </Box>
  );
}
