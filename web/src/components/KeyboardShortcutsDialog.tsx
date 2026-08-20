// file: web/src/components/KeyboardShortcutsDialog.tsx
// version: 1.0.1
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901

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
import { SHORTCUTS } from '../hooks/useKeyboardShortcuts';

interface KeyboardShortcutsDialogProps {
  open: boolean;
  onClose: () => void;
}

export const KeyboardShortcutsDialog: React.FC<KeyboardShortcutsDialogProps> = ({
  open,
  onClose,
}) => {
  const categories = [...new Set(SHORTCUTS.map((s) => s.category))];

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
            <Typography variant="subtitle2" color="text.secondary" sx={{ mb: 1 }}>
              {category}
            </Typography>
            {SHORTCUTS.filter((s) => s.category === category).map((shortcut) => (
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
