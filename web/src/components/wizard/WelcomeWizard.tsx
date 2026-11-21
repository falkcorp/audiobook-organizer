// file: web/src/components/wizard/WelcomeWizard.tsx
// version: 1.6.0
// guid: 8b9c0d1e-2f3a-4b5c-6d7e-8f9a0b1c2d3e
// last-edited: 2026-09-10

// ABO's binding of the split first-run wizard (F9-04 slice 5): the shell's setup frame with books'
// setup steps and state. F9-03 deletes this file when the shell mounts its own frame with every
// enabled module's `setup` steps.

import { useEffect, useState } from 'react';
import * as api from '../../services/api';
import { SetupWizardFrame } from '../setup/SetupWizardFrame';
import { BooksSetupProvider } from '../../modules/books/pages/BooksSetupProvider';
import { booksSetup } from '../../modules/books/pages/booksSetup';
import { useBooksSetup } from '../../modules/books/pages/booksSetupContext';

interface WelcomeWizardProps {
  open: boolean;
  onComplete: () => void;
}

function BoundWizard({ open }: { open: boolean }) {
  const setup = useBooksSetup();
  const [appVersion, setAppVersion] = useState('');

  useEffect(() => {
    api.getAppVersion().then(setAppVersion);
  }, []);

  return (
    <SetupWizardFrame
      open={open}
      title="Welcome to Audiobook Organizer"
      version={appVersion}
      steps={booksSetup.steps}
      canProceed={setup.canProceed}
      onFinish={setup.complete}
      saving={setup.saving}
    />
  );
}

/**
 * WelcomeWizard component for first-run setup
 *
 * Guides users through initial configuration (library path, tools, AI, iTunes, import folders).
 */
export function WelcomeWizard({ open, onComplete }: WelcomeWizardProps) {
  return (
    <BooksSetupProvider open={open} onComplete={onComplete}>
      <BoundWizard open={open} />
    </BooksSetupProvider>
  );
}
