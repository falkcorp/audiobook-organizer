// file: web/src/components/dedup/BulkActionBar.tsx
// version: 1.0.2
// guid: b7a8c9d0-e1f2-3456-abcd-ba7890123456
// last-edited: 2026-08-19

// BulkActionBar renders the floating bottom bar inside UnifiedDedupTab when
// candidates are selected. Provides merge-selected, dismiss-selected, and
// the CERTAIN band auto-merge with a confirm dialog showing the count.

import {
  Box,
  Button,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  Paper,
  Typography,
} from '@mui/material';
import MergeIcon from '@mui/icons-material/MergeType';
import VisibilityOffIcon from '@mui/icons-material/VisibilityOff';
import { useState } from 'react';
import type { DedupBand } from '../../services/api';

interface BulkActionBarProps {
  selectedCount: number;
  total: number;
  /** Current band filter — when CERTAIN, auto-merge requires confirm dialog. */
  bandFilter: DedupBand | null;
  isBusy: boolean;
  onMergeSelected: () => void;
  onDismissSelected: () => void;
  onMergeAllFiltered: () => void;
  onClearSelection: () => void;
}

export function BulkActionBar({
  selectedCount,
  total,
  bandFilter,
  isBusy,
  onMergeSelected,
  onDismissSelected,
  onMergeAllFiltered,
  onClearSelection,
}: BulkActionBarProps) {
  const [confirmMergeAll, setConfirmMergeAll] = useState(false);

  if (selectedCount === 0) return null;

  const handleMergeAllClick = () => {
    // CERTAIN band auto-merge always requires a confirm dialog.
    if (bandFilter === 'CERTAIN' || total > 5) {
      setConfirmMergeAll(true);
    } else {
      onMergeAllFiltered();
    }
  };

  return (
    <>
      <Paper
        elevation={4}
        sx={{
          position: 'sticky',
          bottom: 16,
          zIndex: 10,
          p: 1.5,
          mx: -2,
          display: 'flex',
          alignItems: 'center',
          gap: 2,
          borderRadius: 2,
          bgcolor: 'background.paper',
        }}
        data-testid="bulk-action-bar"
      >
        <Typography variant="body2" fontWeight={600}>
          {selectedCount} selected
        </Typography>

        <Button
          variant="contained"
          color="primary"
          size="small"
          startIcon={isBusy ? <CircularProgress size={14} /> : <MergeIcon />}
          disabled={isBusy}
          onClick={onMergeSelected}
          data-testid="bulk-merge-selected-btn"
        >
          Merge Selected ({selectedCount})
        </Button>

        <Button
          variant="outlined"
          color="inherit"
          size="small"
          startIcon={isBusy ? <CircularProgress size={14} /> : <VisibilityOffIcon />}
          disabled={isBusy}
          onClick={onDismissSelected}
          data-testid="bulk-dismiss-selected-btn"
        >
          Dismiss Selected ({selectedCount})
        </Button>

        <Button
          variant="outlined"
          color="warning"
          size="small"
          startIcon={isBusy ? <CircularProgress size={14} /> : <MergeIcon />}
          disabled={isBusy}
          onClick={handleMergeAllClick}
          data-testid="bulk-merge-all-btn"
        >
          Merge All Filtered ({total})
        </Button>

        <Box sx={{ flex: 1 }} />

        <Button size="small" variant="text" disabled={isBusy} onClick={onClearSelection}>
          Clear
        </Button>
      </Paper>

      {/* Confirm dialog for destructive bulk-merge (CERTAIN band or large count). */}
      <Dialog open={confirmMergeAll} onClose={() => setConfirmMergeAll(false)}>
        <DialogTitle>
          {bandFilter === 'CERTAIN'
            ? 'Auto-merge all CERTAIN candidates?'
            : `Merge all ${total} filtered candidates?`}
        </DialogTitle>
        <DialogContent>
          <DialogContentText>
            {bandFilter === 'CERTAIN' ? (
              <>
                You are about to merge <strong>{total}</strong> CERTAIN-band candidate
                {total === 1 ? '' : 's'} (score ≥ 97). These are the highest-confidence duplicates.
                Each pair becomes one version group. This is irreversible.
              </>
            ) : (
              <>
                You are about to merge <strong>{total}</strong> candidate
                {total === 1 ? '' : 's'} matching the current filter. Each pair becomes one version
                group. This is irreversible.
              </>
            )}
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmMergeAll(false)}>Cancel</Button>
          <Button
            onClick={() => {
              setConfirmMergeAll(false);
              onMergeAllFiltered();
            }}
            color="warning"
            variant="contained"
            data-testid="confirm-merge-all-btn"
          >
            Merge {total}
          </Button>
        </DialogActions>
      </Dialog>
    </>
  );
}
