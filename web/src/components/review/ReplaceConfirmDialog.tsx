// file: web/src/components/review/ReplaceConfirmDialog.tsx
// version: 1.0.0
// guid: c3cb9eb4-fd1b-4979-97c5-d2c603abbd38
// last-edited: 2026-09-27
//
// The one prompt a Replace-mode bulk apply shows (it replaced a bare
// window.confirm). Owner request 2026-09-27: "add a don't show me this again
// button to the prompt when you're replacing files. Then the user takes it in
// their own hands but at least we don't annoy them."
//
// Render it conditionally (mount on open) so the checkbox starts unticked on
// every prompt. Escape and a backdrop click are Cancel, and Cancel never
// persists the "Don't ask me again" choice -- only Replace does.

import { useState } from 'react';
import {
  Button,
  Checkbox,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  FormControlLabel,
} from '@mui/material';
import { replaceConfirmMessage } from './lanes/useMetadataLane';

export interface ReplaceConfirmDialogProps {
  /** How many books the parked bulk apply covers. */
  count: number;
  onConfirm: (dontAskAgain: boolean) => void;
  onCancel: () => void;
}

export function ReplaceConfirmDialog({ count, onConfirm, onCancel }: ReplaceConfirmDialogProps) {
  const [dontAskAgain, setDontAskAgain] = useState(false);
  return (
    <Dialog open onClose={onCancel} data-testid="replace-confirm-dialog">
      <DialogTitle>Replace existing metadata?</DialogTitle>
      <DialogContent>
        <DialogContentText data-testid="replace-confirm-message">
          {replaceConfirmMessage(count)}
        </DialogContentText>
        <FormControlLabel
          sx={{ mt: 1 }}
          control={
            <Checkbox
              checked={dontAskAgain}
              onChange={(e) => setDontAskAgain(e.target.checked)}
              data-testid="replace-confirm-dont-ask"
            />
          }
          label="Don't ask me again"
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={onCancel} data-testid="replace-confirm-cancel">
          Cancel
        </Button>
        <Button
          variant="contained"
          color="warning"
          data-testid="replace-confirm-accept"
          onClick={() => onConfirm(dontAskAgain)}
        >
          {`Replace ${count.toLocaleString()} ${count === 1 ? 'book' : 'books'}`}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
