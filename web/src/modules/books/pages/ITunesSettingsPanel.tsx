// file: web/src/modules/books/pages/ITunesSettingsPanel.tsx
// version: 0.1.0
// guid: 8eb409e1-a459-4fa2-8b32-650bd1ad9e2d
// last-edited: 2026-11-21

// The iTunes Import tab.

import { ITunesImport } from '../../../components/settings/ITunesImport';
import { ITunesTransfer } from '../../../components/settings/ITunesTransfer';

export function ITunesSettingsPanel() {
  return (
    <>
      <ITunesImport />
      <ITunesTransfer />
    </>
  );
}
