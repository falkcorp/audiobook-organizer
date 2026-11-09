// file: web/src/hooks/useBackupHandlers.ts
// version: 1.1.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-11-09

import type { Dispatch, SetStateAction } from 'react';
import * as api from '../services/api';

type BackupNotice = { severity: 'success' | 'error' | 'info'; message: string } | null;

export interface UseBackupHandlersParams {
  setBackups: Dispatch<SetStateAction<api.BackupInfo[]>>;
  setBackupNotice: Dispatch<SetStateAction<BackupNotice>>;
  setBackupsLoading: Dispatch<SetStateAction<boolean>>;
  setRestoreTarget: Dispatch<SetStateAction<api.BackupInfo | null>>;
  setRestoreDialogOpen: Dispatch<SetStateAction<boolean>>;
  setRestoreInProgress: Dispatch<SetStateAction<boolean>>;
  setDeleteBackupTarget: Dispatch<SetStateAction<api.BackupInfo | null>>;
  setDeleteBackupInProgress: Dispatch<SetStateAction<boolean>>;
  setCreateBackupInProgress: Dispatch<SetStateAction<boolean>>;
  setNewFolderPath: Dispatch<SetStateAction<string>>;
  restoreTarget: api.BackupInfo | null;
  restoreVerify: boolean;
  deleteBackupTarget: api.BackupInfo | null;
}

export interface UseBackupHandlersReturn {
  loadBackups: () => Promise<void>;
  handleCreateBackup: () => Promise<void>;
  handleRequestRestore: (backup: api.BackupInfo) => void;
  handleConfirmRestore: () => Promise<void>;
  handleRequestDeleteBackup: (backup: api.BackupInfo) => void;
  handleConfirmDeleteBackup: () => Promise<void>;
  handleFolderBrowserSelect: (path: string, isDir: boolean) => void;
}

/** The notice for a staged restore (POST /backup/restore's 202). */
export function restoreStagedMessage(staged: api.RestoreStagedResponse): string {
  const checked = staged.verified ? ' The backup matched its checksum.' : '';
  return (
    `Restore ${staged.restore_id} staged.${checked} The server is restarting to apply it; ` +
    'this page reloads when it is back.'
  );
}

/**
 * The notice for a refused restore: the server's own reason (a restore or
 * reset already staged, a checksum mismatch, no checksum file to verify
 * against, a backup no longer listed), which says what to do next.
 */
export function restoreFailedMessage(error: unknown): string {
  if (error instanceof api.ApiError && error.message) {
    return `Restore refused: ${error.message}`;
  }
  return 'Failed to restore backup.';
}

export function useBackupHandlers(
  params: UseBackupHandlersParams
): UseBackupHandlersReturn {
  const {
    setBackups,
    setBackupNotice,
    setBackupsLoading,
    setRestoreTarget,
    setRestoreDialogOpen,
    setRestoreInProgress,
    setDeleteBackupTarget,
    setDeleteBackupInProgress,
    setCreateBackupInProgress,
    setNewFolderPath,
    restoreTarget,
    restoreVerify,
    deleteBackupTarget,
  } = params;

  const loadBackups = async () => {
    setBackupsLoading(true);
    try {
      const data = await api.listBackups();
      const sorted = [...(data.backups || [])].sort(
        (a, b) =>
          new Date(b.created_at).getTime() - new Date(a.created_at).getTime()
      );
      setBackups(sorted);
    } catch (error) {
      console.error('Failed to load backups:', error);
      setBackupNotice({
        severity: 'error',
        message: 'Failed to load backups.',
      });
    } finally {
      setBackupsLoading(false);
    }
  };

  const handleCreateBackup = async () => {
    setCreateBackupInProgress(true);
    setBackupNotice(null);
    try {
      await api.createBackup();
      setBackupNotice({
        severity: 'success',
        message: 'Backup created successfully.',
      });
      await loadBackups();
    } catch (error) {
      console.error('Failed to create backup:', error);
      setBackupNotice({
        severity: 'error',
        message: 'Failed to create backup.',
      });
    } finally {
      setCreateBackupInProgress(false);
    }
  };

  const handleRequestRestore = (backup: api.BackupInfo) => {
    setRestoreTarget(backup);
    setRestoreDialogOpen(true);
  };

  const handleConfirmRestore = async () => {
    if (!restoreTarget) return;
    setRestoreInProgress(true);
    setBackupNotice(null);
    try {
      // The server stages the restore and restarts to apply it (202). No
      // reload here: the page would race the restart. The server tells
      // every connected page it is going down (system.shutdown), and the
      // App's reconnect overlay reloads once it answers again, now over the
      // restored database.
      const staged = await api.restoreBackup(restoreTarget.filename, restoreVerify);
      setBackupNotice({
        severity: 'info',
        message: restoreStagedMessage(staged),
      });
      setRestoreDialogOpen(false);
    } catch (error) {
      console.error('Failed to restore backup:', error);
      setBackupNotice({
        severity: 'error',
        message: restoreFailedMessage(error),
      });
    } finally {
      setRestoreInProgress(false);
    }
  };

  const handleRequestDeleteBackup = (backup: api.BackupInfo) => {
    setDeleteBackupTarget(backup);
  };

  const handleConfirmDeleteBackup = async () => {
    if (!deleteBackupTarget) return;
    setDeleteBackupInProgress(true);
    setBackupNotice(null);
    try {
      await api.deleteBackup(deleteBackupTarget.filename);
      setBackupNotice({
        severity: 'success',
        message: 'Backup deleted successfully.',
      });
      setDeleteBackupTarget(null);
      await loadBackups();
    } catch (error) {
      console.error('Failed to delete backup:', error);
      setBackupNotice({
        severity: 'error',
        message: 'Failed to delete backup.',
      });
    } finally {
      setDeleteBackupInProgress(false);
    }
  };

  const handleFolderBrowserSelect = (path: string, isDir: boolean) => {
    if (isDir) {
      setNewFolderPath(path);
    }
  };

  return {
    loadBackups,
    handleCreateBackup,
    handleRequestRestore,
    handleConfirmRestore,
    handleRequestDeleteBackup,
    handleConfirmDeleteBackup,
    handleFolderBrowserSelect,
  };
}
