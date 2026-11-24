// file: web/src/modules/books/pages/signInRequiredDialogs.test.tsx
// version: 0.1.0
// guid: 47e150ec-f2e6-4238-a904-77eeb5c58586
// last-edited: 2026-11-22

// SEC5-8 on the dialogs that call POST /import-paths and POST /backup/restore, and on the setup
// wizard's steps: under auth-off each shows "Turn on sign-in to do this" with a link and disables
// the action; with auth on there is no notice and the action is live.

import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { SignInRequiredNotice } from '../../../components/common/SignInRequiredNotice';
import { SIGN_IN_HELP_URL } from '../../../utils/signInRequired';

const ctx = vi.hoisted(() => ({ settings: {} as Record<string, unknown>, setup: {} as Record<string, unknown> }));
vi.mock('./booksSettingsContext', () => ({ useBooksSettings: () => ctx.settings }));
vi.mock('./booksSetupContext', () => ({ useBooksSetup: () => ctx.setup }));
vi.mock('../../../components/common/ServerFileBrowser', () => ({ ServerFileBrowser: () => null }));
vi.mock('../../../components/library/PurgeSoftDeletedDialog', () => ({ PurgeSoftDeletedDialog: () => null }));
vi.mock('../../../components/tools/ToolsPanel', () => ({ ToolsPanel: () => null }));

import { BooksSettingsDialogs } from './BooksSettingsDialogs';
import { ToolsStep } from './ToolsStep';
import { ImportFoldersStep } from './ImportFoldersStep';
import { LibraryDialogs } from '../../../components/library/LibraryDialogs';

const NOTICE = /Turn on sign-in to do this/;
const gate = (blocked: boolean) => ({
  blocked,
  refuse: () => false,
  notice: blocked ? <SignInRequiredNotice /> : null,
});
const noop = () => {};

const settingsState = (blocked: boolean, open: 'restore' | 'add') => ({
  signInGate: gate(blocked),
  addFolderDialogOpen: open === 'add',
  restoreDialogOpen: open === 'restore',
  newFolderPath: '/mnt/batcave/audio',
  showFolderBrowser: false,
  restoreTarget: { filename: 'backup-1.tar.gz' },
  restoreVerify: true,
  restoreInProgress: false,
  deleteBackupTarget: null,
  cancelScanTarget: null,
  scanErrorTarget: null,
  importDialogOpen: false,
  importKeys: [],
  browserOpen: false,
  blocker: { state: 'unblocked' },
  settings: {},
  setAddFolderDialogOpen: noop,
  setShowFolderBrowser: noop,
  setNewFolderPath: noop,
  setRestoreDialogOpen: noop,
  setRestoreVerify: noop,
  setDeleteBackupTarget: noop,
  setCancelScanTarget: noop,
  setScanErrorTarget: noop,
  setImportDialogOpen: noop,
  handleAddImportFolder: noop,
  handleConfirmRestore: noop,
});

describe('settings dialogs', () => {
  it('import-path add under auth-off: message, link, Add Path disabled', () => {
    ctx.settings = settingsState(true, 'add');
    render(<BooksSettingsDialogs />);
    expect(screen.getByText(NOTICE)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /turn on sign-in/i })).toHaveAttribute('href', SIGN_IN_HELP_URL);
    expect(screen.getByRole('button', { name: 'Add Path' })).toBeDisabled();
  });

  it('import-path add with auth on: no notice, Add Path live', () => {
    ctx.settings = settingsState(false, 'add');
    render(<BooksSettingsDialogs />);
    expect(screen.queryByText(NOTICE)).toBeNull();
    expect(screen.getByRole('button', { name: 'Add Path' })).toBeEnabled();
  });

  it('backup restore under auth-off: message, link, Restore disabled', () => {
    ctx.settings = settingsState(true, 'restore');
    render(<BooksSettingsDialogs />);
    expect(screen.getByText(NOTICE)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Restore' })).toBeDisabled();
  });

  it('backup restore with auth on: no notice, Restore live', () => {
    ctx.settings = settingsState(false, 'restore');
    render(<BooksSettingsDialogs />);
    expect(screen.queryByText(NOTICE)).toBeNull();
    expect(screen.getByRole('button', { name: 'Restore' })).toBeEnabled();
  });
});

describe('library page add-import-path dialog', () => {
  const props = (blocked: boolean) =>
    ({
      addPathDialogOpen: true,
      setAddPathDialogOpen: noop,
      showServerBrowser: false,
      setShowServerBrowser: noop,
      newImportPath: '/mnt/batcave/audio',
      setNewImportPath: noop,
      handleAddImportPath: noop,
      handleServerBrowserSelect: noop,
      addPathGate: gate(blocked),
      selectedAudiobooks: [],
      availableTags: [],
      importFilePaths: [],
      importFilePath: '',
      deleteOptions: { softDelete: false, blockHash: false },
      manualImportPath: '',
      importPaths: [],
      bulkOrganizeProgress: { results: [] },
    }) as unknown as Parameters<typeof LibraryDialogs>[0];

  it('under auth-off: message, link, Add Path disabled', () => {
    render(<LibraryDialogs {...props(true)} />);
    expect(screen.getByText(NOTICE)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add Path' })).toBeDisabled();
  });

  it('with auth on: no notice, Add Path live', () => {
    render(<LibraryDialogs {...props(false)} />);
    expect(screen.queryByText(NOTICE)).toBeNull();
    expect(screen.getByRole('button', { name: 'Add Path' })).toBeEnabled();
  });
});

describe('setup wizard steps', () => {
  it('the tools step shows the notice under auth-off and not with auth on', () => {
    ctx.setup = { toolInstallChoice: 'recommended', setToolInstallChoice: noop, signInGate: gate(true) };
    const { unmount } = render(<ToolsStep onNext={noop} />);
    expect(screen.getByText(NOTICE)).toBeInTheDocument();
    unmount();
    ctx.setup = { toolInstallChoice: 'recommended', setToolInstallChoice: noop, signInGate: gate(false) };
    render(<ToolsStep onNext={noop} />);
    expect(screen.queryByText(NOTICE)).toBeNull();
  });

  it('the import-folders step shows the notice under auth-off and not with auth on', () => {
    ctx.setup = {
      importFolders: [],
      handleOpenImportBrowser: noop,
      handleRemoveImportFolder: noop,
      signInGate: gate(true),
    };
    const { unmount } = render(<ImportFoldersStep />);
    expect(screen.getByText(NOTICE)).toBeInTheDocument();
    unmount();
    ctx.setup = { ...ctx.setup, signInGate: gate(false) };
    render(<ImportFoldersStep />);
    expect(screen.queryByText(NOTICE)).toBeNull();
  });
});
