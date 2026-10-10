// file: web/src/components/audiobooks/OwnerRejectedChip.tsx
// version: 1.0.0
// guid: 6e1b3f72-0c5d-4a98-b2e7-3d9f8a1c5b04
// last-edited: 2026-10-10

import { useState } from 'react';
import { Button, Chip, Stack, Tooltip } from '@mui/material';
import WarningAmberIcon from '@mui/icons-material/WarningAmber';
import type { MetadataCandidate } from '../../services/api';
import * as api from '../../services/api';

interface OwnerRejectedChipProps {
  bookId: string;
  candidate: MetadataCandidate;
  /** Called once the rejection is gone, so the dialog can clear its marker. */
  onUnrejected: (candidate: MetadataCandidate) => void;
  toast: (message: string, severity?: 'success' | 'error' | 'warning' | 'info') => void;
}

/**
 * The "Rejected by you" marker on a metadata search result, with the owner's
 * way back: Undo un-rejects this candidate for this book
 * (POST /metadata/unreject-candidate), after which it can be picked and
 * applied again. Every apply refuses a rejected candidate, so without Undo a
 * rejection would lock the owner out of that record for good.
 */
export function OwnerRejectedChip({
  bookId,
  candidate,
  onUnrejected,
  toast,
}: OwnerRejectedChipProps) {
  const [busy, setBusy] = useState(false);

  const undo = async () => {
    setBusy(true);
    try {
      const res = await api.unrejectCandidate(bookId, candidate);
      onUnrejected(candidate);
      toast(
        res.unrejected
          ? 'Rejection undone: this candidate can be applied again.'
          : 'No rejection was on record for this candidate; it can be applied.',
        res.unrejected ? 'success' : 'info'
      );
    } catch (err) {
      toast(err instanceof Error ? err.message : 'Failed to undo the rejection', 'error');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Stack direction="row" spacing={0.5} sx={{ alignItems: 'center' }}>
      <Tooltip title="You rejected this candidate for this book. Undo the rejection to apply it.">
        <Chip
          icon={<WarningAmberIcon />}
          label="Rejected by you"
          size="small"
          color="error"
          data-testid="owner-rejected-chip"
        />
      </Tooltip>
      <Button
        size="small"
        onClick={undo}
        disabled={busy}
        data-testid="owner-rejected-undo"
        aria-label="Undo rejection"
      >
        Undo
      </Button>
    </Stack>
  );
}
