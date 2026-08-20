// file: web/src/components/review/RegroupPanel.tsx
// version: 1.0.0
// guid: 5a92d0c8-4e17-4b63-8d05-9f2c6a7b1e30
// last-edited: 2026-08-20

/**
 * The regroup lane's full surface: a summary rail and the bucket spine.
 *
 * Assembled here rather than in ReviewWorkspace so the shell keeps its one-line
 * lane branch, matching DupesPanel. There is no filter rail: the queue's only
 * dimension is Kind, and the buckets already ARE that grouping — a rail of kind
 * checkboxes would filter a list that is displayed grouped by the same field.
 */

import { Alert, Box, Chip, IconButton, Stack, Tooltip, Typography } from '@mui/material';
import RefreshIcon from '@mui/icons-material/Refresh';
import { RegroupSpine } from './spine/RegroupSpine';
import type { RegroupLane } from './lanes/useRegroupLane';

export interface RegroupPanelProps {
  regroup: RegroupLane;
}

export function RegroupPanel({ regroup }: RegroupPanelProps) {
  return (
    <Box sx={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' }}>
      <Box
        data-testid="regroup-rail"
        sx={{
          borderBottom: 1,
          borderColor: 'divider',
          px: 2,
          py: 1,
          display: 'flex',
          alignItems: 'center',
          gap: 1,
          flexWrap: 'wrap',
        }}
      >
        <Typography variant="body2" sx={{ color: 'text.secondary', flexGrow: 1 }}>
          Holds the system flagged for a human decision, grouped by type.
        </Typography>
        <Chip size="small" data-testid="regroup-total" label={`${regroup.total} pending`} />
        {regroup.loaded < regroup.total && (
          // Said out loud rather than left to be noticed. Bulk actions are
          // kind-scoped on the server, so they reach holds past this cut.
          <Tooltip title="The queue is longer than one page. Bulk actions still apply to every pending hold of that kind, not just the ones loaded here.">
            <Chip
              size="small"
              color="warning"
              variant="outlined"
              data-testid="regroup-truncated"
              label={`${regroup.loaded} loaded`}
            />
          </Tooltip>
        )}
        <Tooltip title="Reload the queue">
          <IconButton size="small" onClick={regroup.refresh} aria-label="Refresh review queue">
            <RefreshIcon fontSize="small" />
          </IconButton>
        </Tooltip>
      </Box>

      <Box sx={{ flex: 1, minHeight: 0, overflowY: 'auto' }}>
        {regroup.error && (
          <Stack sx={{ p: 2 }}>
            <Alert severity="error" data-testid="regroup-error">
              {regroup.error}
            </Alert>
          </Stack>
        )}
        <RegroupSpine lane={regroup} />
      </Box>
    </Box>
  );
}
