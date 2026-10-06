// file: web/src/components/common/BulkConfirmDialog.tsx
// version: 1.0.0
// guid: 0c9e4f27-6b13-4a8d-a5e2-71d3b8f6c940
// last-edited: 2026-10-06

/**
 * Confirmation for a destructive bulk action whose selection reaches past the
 * current page (useRowSelection's "Select all N matching", or a selection
 * assembled across pages). Always states the count.
 */

import {
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
} from '@mui/material';
import type { ReactNode } from 'react';

export interface BulkConfirmDialogProps {
  open: boolean;
  title: string;
  children: ReactNode;
  confirmLabel: string;
  onCancel: () => void;
  onConfirm: () => void;
  testId?: string;
}

export function BulkConfirmDialog({
  open,
  title,
  children,
  confirmLabel,
  onCancel,
  onConfirm,
  testId = 'bulk-confirm',
}: BulkConfirmDialogProps) {
  return (
    <Dialog open={open} onClose={onCancel} data-testid={testId}>
      <DialogTitle>{title}</DialogTitle>
      <DialogContent>
        <DialogContentText component="div">{children}</DialogContentText>
      </DialogContent>
      <DialogActions>
        <Button onClick={onCancel}>Cancel</Button>
        <Button
          color="warning"
          variant="contained"
          onClick={onConfirm}
          data-testid={`${testId}-btn`}
        >
          {confirmLabel}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
