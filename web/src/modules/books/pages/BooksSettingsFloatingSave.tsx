// file: web/src/modules/books/pages/BooksSettingsFloatingSave.tsx
// version: 0.1.0
// guid: f5f369fa-0ce9-49b0-a898-42f376b888cc
// last-edited: 2026-11-21

// The floating Discard / Save panel, visible only while there are unsaved changes.

import { Paper, Button } from '@mui/material';
import { Save as SaveIcon } from '@mui/icons-material';
import { useBooksSettings } from './booksSettingsContext';
import type { SettingsState } from './settingsState';

export function BooksSettingsFloatingSave() {
  const { configLoaded, handleSave, hasUnsavedChanges, savedSnapshot, setSettings } =
    useBooksSettings();

  if (!hasUnsavedChanges) return null;

  return (
    <Paper
      elevation={6}
      sx={{
        position: 'fixed',
        bottom: 24,
        right: 24,
        zIndex: 1300,
        display: 'flex',
        alignItems: 'center',
        gap: 1.5,
        px: 2.5,
        py: 1.5,
        borderRadius: 3,
        bgcolor: 'background.paper',
        boxShadow: 6,
      }}
    >
      <SaveIcon fontSize="small" color="primary" sx={{ mr: 0.5 }} />
      <Button
        size="small"
        onClick={() => {
          if (savedSnapshot) {
            const prev = JSON.parse(savedSnapshot) as SettingsState;
            setSettings(prev);
          }
        }}
      >
        Discard
      </Button>
      <Button size="small" variant="contained" onClick={handleSave} disabled={!configLoaded}>
        Save
      </Button>
    </Paper>
  );
}
