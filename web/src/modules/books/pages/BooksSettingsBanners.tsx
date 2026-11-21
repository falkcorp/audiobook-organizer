// file: web/src/modules/books/pages/BooksSettingsBanners.tsx
// version: 0.1.0
// guid: 53eceafa-d32f-41a4-b2a6-c0f04bfbf000
// last-edited: 2026-11-21

// The "saved" and "imported" notices shown above the settings tabs.

import { Alert } from '@mui/material';
import { useBooksSettings } from './booksSettingsContext';

export function BooksSettingsBanners() {
  const { importNotice, saved } = useBooksSettings();

  return (
    <>
      {saved && (
        <Alert severity="success" sx={{ mb: 2, flexShrink: 0 }}>
          Settings saved successfully!
        </Alert>
      )}
      {importNotice && (
        <Alert severity="info" sx={{ mb: 2, flexShrink: 0 }}>
          {importNotice}
        </Alert>
      )}
    </>
  );
}
