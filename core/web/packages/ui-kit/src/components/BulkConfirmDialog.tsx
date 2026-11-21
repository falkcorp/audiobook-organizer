// file: core/web/packages/ui-kit/src/components/BulkConfirmDialog.tsx
// version: 0.1.0
// guid: 060dbafa-99d4-4d10-b033-4596d1a2d2c3
// last-edited: 2026-11-21

/**
 * Confirmation for a destructive bulk action whose selection reaches past the
 * current page (useRowSelection's "Select all N matching", or a selection
 * assembled across pages). The caller's `children` state the count.
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
