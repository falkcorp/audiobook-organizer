// file: web/src/modules/books/pages/booksSetup.ts
// version: 0.1.0
// guid: 4f235a67-dc71-4287-8ceb-e2eb31bab15d
// last-edited: 2026-11-21

// F9-04 slice 5: books' contribution to the shell's first-run setup (07 §7.4.2 SetupContribution):
// whether setup is still needed, and its steps. When web resolves @mediacore/shell F9-03 types
// this as SetupContribution and the steps become lazy.

import * as api from '../../../services/api';
import type { SetupContribution } from '../../../components/setup/setupTypes';
import { AiSetupStep } from './AiSetupStep';
import { ImportFoldersStep } from './ImportFoldersStep';
import { ITunesStep } from './ITunesStep';
import { LibraryPathStep } from './LibraryPathStep';
import { ToolsStep } from './ToolsStep';

/**
 * Setup is required until the library has a root directory or setup was marked complete:
 * `!config.root_dir && !config.setup_complete`. A config that cannot be read rejects, and the
 * caller decides what that means.
 */
export async function isBooksSetupRequired(): Promise<boolean> {
  const config = await api.getConfig();
  const setupComplete =
    Boolean(config.root_dir && config.root_dir.trim()) || Boolean(config.setup_complete);
  return !setupComplete;
}

export const booksSetup: SetupContribution = {
  isRequired: isBooksSetupRequired,
  steps: [
    { id: 'library-path', title: 'Library Path', Component: LibraryPathStep },
    { id: 'tools', title: 'Tools', Component: ToolsStep },
    { id: 'ai', title: 'AI Setup (Optional)', Component: AiSetupStep },
    { id: 'itunes', title: 'iTunes Library', Component: ITunesStep },
    { id: 'import-folders', title: 'Import Folders', Component: ImportFoldersStep },
  ],
};
