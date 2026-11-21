// file: web/src/components/KeyboardShortcutsDialog.tsx
// version: 1.0.2
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901

// Moved to @mediacore/ui-kit (F9-05). The dialog takes its table as a prop; this wrapper
// passes ABO's until the shell builds the table from its routes (F9-03).

import React from 'react';
import { KeyboardShortcutsDialog as UiKitKeyboardShortcutsDialog } from '@mediacore/ui-kit';
import { SHORTCUTS } from '../hooks/useKeyboardShortcuts';

interface KeyboardShortcutsDialogProps {
  open: boolean;
  onClose: () => void;
}

export const KeyboardShortcutsDialog: React.FC<KeyboardShortcutsDialogProps> = ({ open, onClose }) => (
  <UiKitKeyboardShortcutsDialog open={open} onClose={onClose} shortcuts={SHORTCUTS} />
);
