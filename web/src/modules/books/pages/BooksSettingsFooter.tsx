// file: web/src/modules/books/pages/BooksSettingsFooter.tsx
// version: 0.1.0
// guid: d859f684-c38e-419b-b121-c3d8817dda70
// last-edited: 2026-11-21

// The sticky Export / Import / Reset / Save bar under the tab panels.

import { Box, Button } from '@mui/material';
import { Save as SaveIcon, RestartAlt as RestartAltIcon } from '@mui/icons-material';
import { useBooksSettings } from './booksSettingsContext';

export function BooksSettingsFooter() {
  const {
    configLoaded,
    exportInProgress,
    handleExportSettings,
    handleImportClick,
    handleImportFileChange,
    handleReset,
    handleSave,
    importInProgress,
    importInputRef,
  } = useBooksSettings();

  return (
    <Box
      sx={{
        position: 'sticky',
        bottom: 0,
        p: 2,
        display: 'flex',
        gap: 2,
        justifyContent: 'flex-end',
        borderTop: 1,
        borderColor: 'divider',
        bgcolor: 'background.paper',
        zIndex: 10,
        boxShadow: '0 -2px 8px rgba(0,0,0,0.3)',
      }}
    >
      <input
        type="file"
        accept="application/json"
        ref={importInputRef}
        onChange={handleImportFileChange}
        style={{ display: 'none' }}
      />
      <Button variant="outlined" onClick={handleExportSettings} disabled={exportInProgress}>
        {exportInProgress ? 'Exporting...' : 'Export Settings'}
      </Button>
      <Button variant="outlined" onClick={handleImportClick} disabled={importInProgress}>
        {importInProgress ? 'Importing...' : 'Import Settings'}
      </Button>
      <Button variant="outlined" startIcon={<RestartAltIcon />} onClick={handleReset}>
        Reset to Defaults
      </Button>
      <Button
        variant="contained"
        startIcon={<SaveIcon />}
        onClick={handleSave}
        disabled={!configLoaded}
      >
        Save Settings
      </Button>
    </Box>
  );
}
