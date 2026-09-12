// file: web/src/hooks/useImportFolderHandlers.ts
// version: 1.2.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-09-12

import type { Dispatch, MutableRefObject, SetStateAction } from 'react';
import * as api from '../services/api';
import type { ScanStatus, ScanErrorTarget } from './useSettingsHandlers';

/** How often an import-path scan's operation is polled. */
const SCAN_POLL_INTERVAL_MS = 1000;

/**
 * Failed polls in a row before the row stops polling and reports an error. A
 * 404 stops immediately (see OPERATION_GONE_MESSAGE); this bounds every other
 * failure -- a server that is down, a 500 on every read -- to about ten seconds
 * of retries instead of a row that says "scanning" forever.
 */
export const MAX_CONSECUTIVE_POLL_FAILURES = 10;

/** Shown when the operation row is gone (Discard, purge) while the page polls it. */
export const OPERATION_GONE_MESSAGE =
  'The scan operation no longer exists (it was discarded or purged), so its result cannot be read.';

/**
 * Log lines read back when a scan finishes. The scanner lists at most 25
 * failed files plus a notice and a summary line, so this covers them alongside
 * the operation's own lifecycle lines.
 */
const SCAN_ERROR_LOG_LIMIT = 200;

/**
 * interruptedScanState maps an `interrupted*` operation status to the neutral
 * row the page shows, and whether polling should continue. Returns null for
 * any other status.
 *
 * Only `interrupted_quiesced` keeps polling: the registry re-queues that same
 * row, under the same id, when the scan stand-down is released
 * (resumeQuiescedOp -> resumeRestart), so the poll sees it go back to running
 * with no restart. `interrupted_ask` waits for a person, `interrupted_dropped`
 * never resumes, and the legacy `interrupted` / `interrupted_restart` spellings
 * resume only at boot -- polling any of those would spin until the page closes.
 *
 * None of these is an error, and `api.isOperationTerminal` is deliberately left
 * counting them as terminal: waitForOperation relies on that to return.
 */
export function interruptedScanState(
  status: string
): { message: string; keepPolling: boolean } | null {
  if (!status.startsWith('interrupted')) return null;
  switch (status) {
    case 'interrupted_quiesced':
      return {
        keepPolling: true,
        message:
          'Scan interrupted; it will resume when the scan stand-down lifts or the server restarts.',
      };
    case 'interrupted_ask':
      return {
        keepPolling: false,
        message: 'Scan interrupted; it is waiting for a decision on whether to resume.',
      };
    case 'interrupted_dropped':
      return {
        keepPolling: false,
        message: 'Scan interrupted and will not resume; start a new scan.',
      };
    default:
      return {
        keepPolling: false,
        message: 'Scan interrupted; it will resume after the server restarts.',
      };
  }
}

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

    let inFlight = false;
    let consecutiveFailures = 0;
    // `interval` is the const assigned below; these closures only run from its
    // own timer callback, so it is always initialised by the time they read it.
    // The identity check keeps a stale poll from a re-scanned folder from
    // clearing the newer interval.
    const stopPolling = () => {
      window.clearInterval(interval);
      if (scanIntervalsRef.current[folder.id] === interval) {
        delete scanIntervalsRef.current[folder.id];
      }
    };
    // A progress write while the scan is still live. A cancel from this page
    // already wrote 'cancelled'; keep it.
    const writeLive = (next: ScanStatus) => {
      setScanStatuses((prev) => {
        if (prev[folder.id]?.status === 'cancelled') return prev;
        return { ...prev, [folder.id]: next };
      });
    };
    const stopWithError = (message: string) => {
      stopPolling();
      setScanStatuses((prev) => ({
        ...prev,
        [folder.id]: {
          status: 'error',
          scanned: prev[folder.id]?.scanned ?? 0,
          total: prev[folder.id]?.total ?? 0,
          operationId,
          message,
          errors: [message],
        },
      }));
    };
    const poll = async () => {
      if (inFlight) return;
      inFlight = true;
      try {
        let op: api.OperationV2;
        try {
          op = await api.getOperationV2(operationId);
        } catch (error) {
          console.error('Failed to poll scan operation:', error);
          // The row is gone (Discard, purge): no later poll can succeed.
          if (error instanceof api.ApiError && error.status === 404) {
            stopWithError(OPERATION_GONE_MESSAGE);
            return;
          }
          // Anything else may be transient, but not forever.
          consecutiveFailures += 1;
          if (consecutiveFailures >= MAX_CONSECUTIVE_POLL_FAILURES) {
            const reason = error instanceof Error ? error.message : String(error);
            stopWithError(
              `Stopped checking on the scan after ${consecutiveFailures} failed attempts in a row: ${reason}`
            );
          }
          return;
        }
        consecutiveFailures = 0;

        const scanned = op.progress_current ?? 0;
        const total = op.progress_total ?? 0;

        const interrupted = interruptedScanState(op.status);
        if (interrupted) {
          const row: ScanStatus = {
            status: 'interrupted',
            scanned,
            total,
            operationId,
            message: interrupted.message,
            errors: op.error_message ? [op.error_message] : [],
          };
          if (interrupted.keepPolling) {
            writeLive(row);
            return;
          }
          stopPolling();
          const logged = await loadScanErrors(operationId);
          setScanStatuses((prev) => ({
            ...prev,
            [folder.id]: { ...row, errors: [...(row.errors ?? []), ...logged] },
          }));
          return;
        }

        if (!api.isOperationTerminal(op.status)) {
          writeLive({ status: 'scanning', scanned, total, operationId, errors: [] });
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
      } finally {
        inFlight = false;
      }
    };
    const interval = window.setInterval(() => {
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
