// file: web/src/modules/books/pages/booksSettingsContext.ts
// version: 0.1.0
// guid: d6a1e71a-4f25-4ce5-9127-982bf58d31aa
// last-edited: 2026-11-21

import { createContext, useContext } from 'react';
import type { useBooksSettingsState } from './useBooksSettingsState';

export type BooksSettingsState = ReturnType<typeof useBooksSettingsState>;

export const BooksSettingsContext = createContext<BooksSettingsState | null>(null);

export function useBooksSettings(): BooksSettingsState {
  const value = useContext(BooksSettingsContext);
  if (!value) throw new Error('useBooksSettings must be used inside BooksSettingsProvider');
  return value;
}
