// file: core/web/packages/ui-kit/src/components/KeyboardShortcutsDialog.tsx
// version: 0.1.0
// guid: 20b99b84-7f27-4ed7-9012-f1c2aed3bb94
// last-edited: 2026-11-21

import React from 'react';
import {
  Dialog,
  DialogTitle,
  DialogContent,
  Typography,
  Box,
  Chip,
  IconButton,
  Divider,
} from '@mui/material';
import CloseIcon from '@mui/icons-material/Close';

/** One row of the help table. `keys` lists alternatives joined with " or ", e.g. "/ or Ctrl+K" or "g l". */
export interface ShortcutHelpEntry {
  keys: string;
  description: string;
  category: string;
}

export interface KeyboardShortcutsDialogProps {
  open: boolean;
  onClose: () => void;
  /** The table to show. The host builds it (the shell from its routes and nav entries); this component names no route. */
  shortcuts: readonly ShortcutHelpEntry[];
}

export const KeyboardShortcutsDialog: React.FC<KeyboardShortcutsDialogProps> = ({
  open,
  onClose,
  shortcuts,
}) => {
  const categories = [...new Set(shortcuts.map((s) => s.category))];

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        Keyboard Shortcuts
        <IconButton onClick={onClose} size="small" aria-label="close">
          <CloseIcon />
        </IconButton>
      </DialogTitle>
      <DialogContent>
        {categories.map((category, ci) => (
          <Box key={category} sx={{ mb: 2 }}>
            {ci > 0 && <Divider sx={{ mb: 2 }} />}
            <Typography
              variant="subtitle2"
              sx={{
                color: 'text.secondary',
                mb: 1,
              }}
            >
              {category}
            </Typography>
            {shortcuts.filter((s) => s.category === category).map((shortcut) => (
              <Box
                key={shortcut.keys}
                sx={{
                  display: 'flex',
                  justifyContent: 'space-between',
                  alignItems: 'center',
                  py: 0.75,
                }}
              >
                <Typography variant="body2">{shortcut.description}</Typography>
                <Box sx={{ display: 'flex', gap: 0.5 }}>
                  {shortcut.keys.split(' or ').map((k) => (
                    <Chip
                      key={k}
                      label={k}
                      size="small"
                      variant="outlined"
                      sx={{ fontFamily: 'monospace', fontSize: '0.75rem' }}
                    />
                  ))}
                </Box>
              </Box>
            ))}
          </Box>
        ))}
      </DialogContent>
    </Dialog>
  );
};
