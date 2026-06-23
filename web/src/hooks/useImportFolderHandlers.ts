// file: web/src/hooks/useImportFolderHandlers.ts
// version: 1.0.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-06-23

import type { Dispatch, MutableRefObject, SetStateAction } from 'react';
import * as api from '../services/api';
import type { ScanStatus, ScanErrorTarget } from './useSettingsHandlers';

export interface UseImportFolderHandlersParams {
  setImportFolders: Dispatch<SetStateAction<api.ImportPath[]>>;
  setScanStatuses: Dispatch<SetStateAction<Record<number, ScanStatus>>>;
  setCancelScanTarget: Dispatch<SetStateAction<api.ImportPath | null>>;
  setScanErrorTarget: Dispatch<SetStateAction<ScanErrorTarget | null>>;
  setNewFolderPath: Dispatch<SetStateAction<string>>;
  setShowFolderBrowser: Dispatch<SetStateAction<boolean>>;
  setAddFolderDialogOpen: Dispatch<SetStateAction<boolean>>;
  scanIntervalsRef: MutableRefObject<Record<number, number>>;
  cancelScanTarget: api.ImportPath | null;
  scanStatuses: Record<number, ScanStatus>;
  newFolderPath: string;
}

export interface UseImportFolderHandlersReturn {
  loadImportFolders: () => Promise<void>;
  handleAddImportFolder: () => Promise<void>;
  handleRemoveImportFolder: (id: number) => Promise<void>;
  handleScanImportFolder: (folder: api.ImportPath) => Promise<void>;
  handleRequestCancelScan: (folder: api.ImportPath) => void;
  handleConfirmCancelScan: () => Promise<void>;
  handleViewScanErrors: (folder: api.ImportPath, status: ScanStatus) => void;
}

export function useImportFolderHandlers(
  params: UseImportFolderHandlersParams
): UseImportFolderHandlersReturn {
  const {
    setImportFolders,
    setScanStatuses,
    setCancelScanTarget,
    setScanErrorTarget,
    setNewFolderPath,
    setShowFolderBrowser,
    setAddFolderDialogOpen,
    scanIntervalsRef,
    cancelScanTarget,
    scanStatuses,
    newFolderPath,
  } = params;

  const loadImportFolders = async () => {
    try {
      const folders = await api.getImportPaths();
      setImportFolders(folders);
    } catch (error) {
      console.error('Failed to load import folders:', error);
    }
  };

  const handleAddImportFolder = async () => {
    if (!newFolderPath.trim()) return;
    try {
      const folder = await api.addImportPath(
        newFolderPath,
        newFolderPath.split('/').pop() || 'Import Folder'
      );
      setImportFolders((prev) => [...prev, folder]);
      setNewFolderPath('');
      setShowFolderBrowser(false);
      setAddFolderDialogOpen(false);
    } catch (error) {
      console.error('Failed to add import folder:', error);
    }
  };

  const handleRemoveImportFolder = async (id: number) => {
    try {
      await api.removeImportPath(id);
      setImportFolders((prev) => prev.filter((f) => f.id !== id));
    } catch (error) {
      console.error('Failed to remove import folder:', error);
    }
  };

  const handleScanImportFolder = async (folder: api.ImportPath) => {
    setScanStatuses((prev) => ({
      ...prev,
      [folder.id]: {
        status: 'scanning',
        scanned: 0,
        total: prev[folder.id]?.total || 0,
      },
    }));

    let total = 50;
    let errors: string[] = [];
    let operationId: string | undefined;

    try {
      const response = await api.startScan(folder.path);
      if (typeof response.total === 'number') {
        total = response.total;
      }
      if (Array.isArray(response.errors)) {
        errors = response.errors;
      }
      operationId = response.id;
    } catch (error) {
      console.error('Failed to scan import folder:', error);
      const message =
        error instanceof Error ? error.message : 'Scan failed.';
      setScanStatuses((prev) => ({
        ...prev,
        [folder.id]: {
          status: 'error',
          scanned: 0,
          total: 0,
          errors: [message],
        },
      }));
      return;
    }

    setScanStatuses((prev) => ({
      ...prev,
      [folder.id]: {
        status: 'scanning',
        scanned: 0,
        total,
        operationId,
        errors,
      },
    }));

    let scanned = 0;
    const increment = Math.max(1, Math.ceil(total / 10));
    if (scanIntervalsRef.current[folder.id]) {
      window.clearInterval(scanIntervalsRef.current[folder.id]);
    }
    const interval = window.setInterval(() => {
      scanned += increment;
      setScanStatuses((prev) => ({
        ...prev,
        [folder.id]: {
          status: scanned >= total ? 'complete' : 'scanning',
          scanned: Math.min(scanned, total),
          total,
          operationId,
          errors,
        },
      }));
      if (scanned >= total) {
        window.clearInterval(interval);
        delete scanIntervalsRef.current[folder.id];
      }
    }, 300);

    scanIntervalsRef.current[folder.id] = interval;
  };

  const handleRequestCancelScan = (folder: api.ImportPath) => {
    setCancelScanTarget(folder);
  };

  const handleConfirmCancelScan = async () => {
    if (!cancelScanTarget) return;
    const target = cancelScanTarget;
    setCancelScanTarget(null);
    const status = scanStatuses[target.id];
    if (!status) return;
    const interval = scanIntervalsRef.current[target.id];
    if (interval) {
      window.clearInterval(interval);
      delete scanIntervalsRef.current[target.id];
    }
    if (status.operationId) {
      try {
        await api.cancelOperation(status.operationId);
      } catch (error) {
        console.error('Failed to cancel scan operation:', error);
      }
    }
    setScanStatuses((prev) => ({
      ...prev,
      [target.id]: {
        ...status,
        status: 'cancelled',
      },
    }));
  };

  const handleViewScanErrors = (
    folder: api.ImportPath,
    status: ScanStatus
  ) => {
    if (!status.errors?.length) return;
    setScanErrorTarget({
      path: folder.path,
      errors: status.errors,
    });
  };

  return {
    loadImportFolders,
    handleAddImportFolder,
    handleRemoveImportFolder,
    handleScanImportFolder,
    handleRequestCancelScan,
    handleConfirmCancelScan,
    handleViewScanErrors,
  };
}
