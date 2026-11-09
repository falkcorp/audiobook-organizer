// file: web/src/hooks/__tests__/useBackupHandlers.test.ts
// version: 1.0.0
// guid: 11a15723-c35e-4686-891d-4cea739b32a9
// last-edited: 2026-11-09

import { act, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../services/api';
import {
  restoreFailedMessage,
  restoreStagedMessage,
  useBackupHandlers,
} from '../useBackupHandlers';

vi.mock('../../services/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../services/api')>();
  return {
    ...actual,
    restoreBackup: vi.fn(),
  };
});

const target = { filename: 'backup-batcave-20261109.tar.gz' } as api.BackupInfo;

function harness() {
  const setBackupNotice = vi.fn();
  const setRestoreDialogOpen = vi.fn();
  const setRestoreInProgress = vi.fn();
  const { result } = renderHook(() =>
    useBackupHandlers({
      setBackups: vi.fn(),
      setBackupNotice,
      setBackupsLoading: vi.fn(),
      setRestoreTarget: vi.fn(),
      setRestoreDialogOpen,
      setRestoreInProgress,
      setDeleteBackupTarget: vi.fn(),
      setDeleteBackupInProgress: vi.fn(),
      setCreateBackupInProgress: vi.fn(),
      setNewFolderPath: vi.fn(),
      restoreTarget: target,
      restoreVerify: true,
      deleteBackupTarget: null,
    })
  );
  return { result, setBackupNotice, setRestoreDialogOpen, setRestoreInProgress };
}

describe('useBackupHandlers restore (POST /backup/restore stages and restarts)', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.mocked(api.restoreBackup).mockReset();
  });

  it('reports the staged restore and the restart, closes the dialog and does not reload', async () => {
    const staged: api.RestoreStagedResponse = {
      message: 'restore staged; the service restarts now to apply it',
      restart_required: true,
      restore_id: '20261109T010203Z-0a1b2c3d',
      verified: true,
      verify_requested: true,
    };
    vi.mocked(api.restoreBackup).mockResolvedValue(staged);
    const reload = vi.fn();
    vi.spyOn(window, 'location', 'get').mockReturnValue({ ...window.location, reload });
    const h = harness();

    await act(async () => {
      await h.result.current.handleConfirmRestore();
    });

    expect(api.restoreBackup).toHaveBeenCalledWith(target.filename, true);
    expect(h.setBackupNotice).toHaveBeenLastCalledWith({
      severity: 'info',
      message: restoreStagedMessage(staged),
    });
    expect(restoreStagedMessage(staged)).toContain(staged.restore_id);
    expect(restoreStagedMessage(staged)).toContain('restarting');
    expect(h.setRestoreDialogOpen).toHaveBeenCalledWith(false);
    expect(h.setRestoreInProgress).toHaveBeenLastCalledWith(false);
    // The reload would race the restart; the App's reconnect overlay
    // reloads once the server answers again.
    expect(reload).not.toHaveBeenCalled();
  });

  it('shows the server reason for a refused restore and keeps the dialog open', async () => {
    vi.mocked(api.restoreBackup).mockRejectedValue(
      new api.ApiError('a restore or reset is already staged (restore_pending)', 409)
    );
    vi.spyOn(console, 'error').mockImplementation(() => {});
    const h = harness();

    await act(async () => {
      await h.result.current.handleConfirmRestore();
    });

    expect(h.setBackupNotice).toHaveBeenLastCalledWith({
      severity: 'error',
      message: 'Restore refused: a restore or reset is already staged (restore_pending)',
    });
    expect(h.setRestoreDialogOpen).not.toHaveBeenCalledWith(false);
    expect(h.setRestoreInProgress).toHaveBeenLastCalledWith(false);
  });

  it('says only that the restore failed when the error carries no server reason', () => {
    expect(restoreFailedMessage(new TypeError('Failed to fetch'))).toBe(
      'Failed to restore backup.'
    );
    expect(restoreFailedMessage(new api.ApiError('', 500))).toBe('Failed to restore backup.');
  });

  it('mentions the checksum only when the archive was verified', () => {
    const base = {
      message: '',
      restart_required: true,
      restore_id: 'r1',
      verify_requested: false,
    };
    expect(restoreStagedMessage({ ...base, verified: false })).not.toContain('checksum');
    expect(restoreStagedMessage({ ...base, verified: true, verify_requested: true })).toContain(
      'checksum'
    );
  });
});
