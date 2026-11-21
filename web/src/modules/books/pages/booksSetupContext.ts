// file: web/src/modules/books/pages/booksSetupContext.ts
// version: 0.1.0
// guid: a0becd2f-27a5-42c9-ad15-5484f21cda0d
// last-edited: 2026-11-21

// F9-04 slice 5: the context that carries books' first-run setup state from its provider to the
// steps and the browser dialogs (07 §7.4.2: a module-wide context is a `providers` entry).

import { createContext, useContext } from 'react';
import type { useBooksSetupState } from './useBooksSetupState';

export type BooksSetupState = ReturnType<typeof useBooksSetupState>;

export const BooksSetupContext = createContext<BooksSetupState | null>(null);

export function useBooksSetup(): BooksSetupState {
  const value = useContext(BooksSetupContext);
  if (!value) throw new Error('useBooksSetup must be used inside BooksSetupProvider');
  return value;
}
