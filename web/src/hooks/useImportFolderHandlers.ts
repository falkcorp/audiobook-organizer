// file: web/src/hooks/useImportFolderHandlers.ts
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-09-12

import type { Dispatch, MutableRefObject, SetStateAction } from 'react';
import * as api from '../services/api';
import type { ScanStatus, ScanErrorTarget } from './useSettingsHandlers';

/** How often an import-path scan's operation is polled. */
const SCAN_POLL_INTERVAL_MS = 1000;

/**
 * Log lines read back when a scan finishes. The scanner lists at most 25
 * failed files plus a notice and a summary line, so this covers them alongside
 * the operation's own lifecycle lines.
 */
const SCAN_ERROR_LOG_LIMIT = 200;

/**
 * scanErrorsFromLogs turns a scan operation's log into the per-file error list
 * shown by "View Errors": one `path: reason` entry per failure line (attrs
 * carry file_path / reason), plus a trailing count when the scanner's summary
 * says more files failed than it listed.
 */
export function scanErrorsFromLogs(logs: api.OperationLog[]): string[] {
  const out: string[] = [];
  let omitted = 0;
  for (const log of logs) {
    const attrs = log.attrs ?? {};
    const filePath = attrs.file_path;
    if (typeof filePath === 'string' && filePath !== '') {
      const reason = typeof attrs.reason === 'string' ? attrs.reason : log.message;
      out.push(`${filePath}: ${reason}`);
      continue;
    }
    const failed = attrs.files_failed;
    const listed = attrs.files_listed;
    if (typeof failed === 'number' && typeof listed === 'number') {
      omitted = Math.max(0, failed - listed);
    }
  }
  if (omitted > 0) {
    out.push(`...and ${omitted} more file(s) failed (not listed)`);
  }
  return out;
}

async function loadScanErrors(operationId: string): Promise<string[]> {
  try {
    return scanErrorsFromLogs(await api.getOperationLogs(operationId, SCAN_ERROR_LOG_LIMIT));
  } catch (error) {
    // Reported, not swallowed: an unreadable log must not read as a clean scan.
    console.error('Failed to load scan errors:', error);
    const message = error instanceof Error ? error.message : String(error);
    return [`The scan finished, but its error list could not be loaded: ${message}`];
  }
}

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

    // The trigger answers an operation id and nothing else: starting a scan is
    // asynchronous. Progress, the terminal status and the per-file failures are
    // read back off the operation itself -- GET /operations/v2/:id for the row,
    // and its log for the failures, which the scanner writes as warn lines
    // carrying file_path / reason attrs (a bounded sample plus a summary line
    // with the total; see internal/scanner/scan_failures.go).
    //
    // This replaced a timer that counted to a hard-coded total of 50 in 300ms
    // steps and declared the scan complete after three seconds whatever the
    // scan was doing, with `errors` seeded as a permanently empty array.
    let operationId: string;

    try {
      const response = await api.startScan(folder.path);
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
        total: 0,
        operationId,
        errors: [],
      },
    }));

    if (scanIntervalsRef.current[folder.id]) {
      window.clearInterval(scanIntervalsRef.current[folder.id]);
    }

    let interval = 0;
    let inFlight = false;
    const stopPolling = () => {
      window.clearInterval(interval);
      if (scanIntervalsRef.current[folder.id] === interval) {
        delete scanIntervalsRef.current[folder.id];
      }
    };
    const poll = async () => {
      if (inFlight) return;
      inFlight = true;
      try {
        const op = await api.getOperationV2(operationId);
        const scanned = op.progress_current ?? 0;
        const total = op.progress_total ?? 0;
        if (!api.isOperationTerminal(op.status)) {
          setScanStatuses((prev) => {
            // A cancel from this page already wrote 'cancelled'; keep it.
            if (prev[folder.id]?.status === 'cancelled') return prev;
            return {
              ...prev,
              [folder.id]: { status: 'scanning', scanned, total, operationId, errors: [] },
            };
          });
          return;
        }
        stopPolling();
        const errors = await loadScanErrors(operationId);
        let status: ScanStatus['status'] = 'error';
        if (op.status === 'completed') status = 'complete';
        else if (op.status === 'canceled') status = 'cancelled';
        if (status === 'error') {
          errors.unshift(op.error_message || `Scan ended with status ${op.status}.`);
        }
        setScanStatuses((prev) => ({
          ...prev,
          [folder.id]: { status, scanned, total, operationId, errors },
        }));
      } catch (error) {
        // A transient read failure leaves the row scanning; the next tick retries.
        console.error('Failed to poll scan operation:', error);
      } finally {
        inFlight = false;
      }
    };
    interval = window.setInterval(() => {
      void poll();
    }, SCAN_POLL_INTERVAL_MS);

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
