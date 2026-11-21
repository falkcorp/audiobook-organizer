// file: web/src/modules/books/pages/BooksSetupProvider.tsx
// version: 0.1.0
// guid: 78bc6211-3c7f-4485-8704-a22d1c5393fc
// last-edited: 2026-11-21

// F9-04 slice 5: holds books' first-run setup state for the lifetime of the wizard and renders the
// folder and file browser dialogs its steps open. It stays mounted while the wizard is closed,
// as the WelcomeWizard always did, so the typed values survive moving between steps.

import type { ReactNode } from 'react';
import { BooksSetupContext } from './booksSetupContext';
import { SetupBrowserDialogs } from './SetupBrowserDialogs';
import { useBooksSetupState } from './useBooksSetupState';

export function BooksSetupProvider({
  open,
  onComplete,
  children,
}: {
  open: boolean;
  onComplete: () => void;
  children: ReactNode;
}) {
  const value = useBooksSetupState({ open, onComplete });
  return (
    <BooksSetupContext.Provider value={value}>
      {children}
      <SetupBrowserDialogs />
    </BooksSetupContext.Provider>
  );
}
