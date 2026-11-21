// file: web/src/modules/books/pages/BooksSettingsProvider.tsx
// version: 0.1.0
// guid: a5707eb0-55b5-4a69-8e40-35aa43b781ed
// last-edited: 2026-11-21

// Holds the Settings page's state for every books panel, and renders the pieces that
// live outside the tab frame: the floating save panel and the dialogs.

import type { ReactNode } from 'react';
import { BooksSettingsContext } from './booksSettingsContext';
import { useBooksSettingsState } from './useBooksSettingsState';
import { BooksSettingsDialogs } from './BooksSettingsDialogs';
import { BooksSettingsFloatingSave } from './BooksSettingsFloatingSave';

export function BooksSettingsProvider({ children }: { children: ReactNode }) {
  const state = useBooksSettingsState();
  return (
    <BooksSettingsContext.Provider value={state}>
      {children}
      <BooksSettingsFloatingSave />
      <BooksSettingsDialogs />
    </BooksSettingsContext.Provider>
  );
}
